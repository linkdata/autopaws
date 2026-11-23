package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type waitInput struct {
	WaitFor       *string `json:"wait_for,omitempty" jsonschema:"Optional visual condition to check after input using the configured vision endpoint. Returns the checked screenshot; model acceptance does not guarantee completion."`
	WaitTimeoutMS *int64  `json:"wait_timeout_ms,omitempty" jsonschema:"Observation budget including vision calls, 1..60000 milliseconds, default 5000. Requires wait_for. Timeout preserves the successful input result; do not replay input automatically."`
}

func (input *waitInput) takeWait() waitInput {
	wait := *input
	*input = waitInput{}
	return wait
}

func validateWait(input waitInput) error {
	if input.WaitFor == nil {
		if input.WaitTimeoutMS != nil {
			return errors.New("wait_timeout_ms requires wait_for")
		}
		return nil
	}
	if strings.TrimSpace(*input.WaitFor) == "" || len(*input.WaitFor) > 2048 {
		return errors.New("wait_for must contain 1..2048 bytes of nonblank text")
	}
	if input.WaitTimeoutMS != nil && (*input.WaitTimeoutMS < 1 || *input.WaitTimeoutMS > 60000) {
		return errors.New("wait_timeout_ms must be in 1..60000")
	}
	return nil
}

type observationResult struct {
	Input struct {
		Sent       bool            `json:"sent"`
		Result     json.RawMessage `json:"result"`
		DurationMS float64         `json:"duration_ms"`
	} `json:"input"`
	Observation struct {
		Status     string  `json:"status"`
		Condition  string  `json:"condition"`
		DurationMS float64 `json:"duration_ms"`
		Detail     string  `json:"detail,omitempty"`
	} `json:"observation"`
	Predicate struct {
		Satisfied   *bool   `json:"satisfied"`
		Model       string  `json:"model"`
		Evaluations int     `json:"evaluations"`
		DurationMS  float64 `json:"duration_ms"`
	} `json:"predicate"`
	Frame *checkedFrame `json:"frame,omitempty"`
}

type checkedFrame struct {
	ID          string  `json:"id"`
	SHA256      string  `json:"sha256"`
	ImageWidth  int     `json:"width"`
	ImageHeight int     `json:"height"`
	AgeMS       float64 `json:"age_ms"`
}

// observe starts only after successful input. Errors during observation retain
// that input outcome. Parent cancellation still follows MCP cancellation rules.
func (owner *toolServer) observe(ctx context.Context, request *mcp.CallToolRequest, input waitInput, action *mcp.CallToolResult, actionDuration time.Duration) (*mcp.CallToolResult, any, error) {
	budget := 5 * time.Second
	if input.WaitTimeoutMS != nil {
		budget = time.Duration(*input.WaitTimeoutMS) * time.Millisecond
	}
	waitCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	state := owner.state(request.Session)
	epoch := state.beginCapture()
	started := time.Now()
	var report observationResult
	report.Input.Sent = true
	report.Input.DurationMS = float64(actionDuration) / float64(time.Millisecond)
	if len(action.Content) == 1 {
		if text, ok := action.Content[0].(*mcp.TextContent); ok && json.Valid([]byte(text.Text)) {
			report.Input.Result = json.RawMessage(text.Text)
		}
	}
	report.Observation.Condition = *input.WaitFor
	report.Observation.Status = "timeout"
	report.Predicate.Model = owner.options.Vision.model
	var checked capturedScreen
	var checkedAt time.Time
	var previous []byte
	var lastCheck time.Time
	for waitCtx.Err() == nil {
		frame, err := owner.capture(waitCtx, "wait_for")
		capturedAt := time.Now()
		if err != nil {
			if waitCtx.Err() == nil {
				report.Observation.Status, report.Observation.Detail = "capture_error", err.Error()
			}
			break
		}
		decoded, thumbnail, err := prepareVisionFrame(waitCtx, frame.jpeg)
		if err != nil {
			if waitCtx.Err() == nil {
				report.Observation.Status, report.Observation.Detail = "capture_error", err.Error()
			}
			break
		}
		if !lastCheck.IsZero() && !visualChange(previous, thumbnail) && time.Since(lastCheck) < 500*time.Millisecond {
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-waitCtx.Done():
			case <-timer.C:
			}
			timer.Stop()
			continue
		}
		prepared, err := owner.options.Vision.image(waitCtx, frame.jpeg, decoded)
		if err != nil {
			if waitCtx.Err() == nil {
				report.Observation.Status, report.Observation.Detail = "capture_error", err.Error()
			}
			break
		}
		checkStart := time.Now()
		satisfied, err := owner.options.Vision.check(waitCtx, prepared, *input.WaitFor)
		report.Predicate.DurationMS += float64(time.Since(checkStart)) / float64(time.Millisecond)
		report.Predicate.Evaluations++
		if waitCtx.Err() != nil {
			break
		}
		if err != nil {
			if waitCtx.Err() == nil {
				report.Observation.Status = "analysis_error"
				if errors.Is(err, errInvalidVisionOutput) {
					report.Observation.Status = "invalid_output"
				}
				report.Observation.Detail = err.Error()
			}
			break
		}
		checked, checkedAt = frame, capturedAt
		report.Predicate.Satisfied = &satisfied
		digest := sha256.Sum256(frame.jpeg)
		report.Frame = &checkedFrame{
			ID: fmt.Sprintf("%s:%d", owner.instance, owner.observations.Add(1)), SHA256: hex.EncodeToString(digest[:]),
			ImageWidth: frame.dimensions.width, ImageHeight: frame.dimensions.height,
		}
		if satisfied {
			report.Observation.Status = "condition_met"
			break
		}
		previous, lastCheck = thumbnail, time.Now()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if report.Frame != nil {
		if err := state.commit(ctx, epoch, checked.dimensions); err != nil {
			report.Observation.Status, report.Observation.Detail = "capture_changed", err.Error()
			report.Frame = nil
			report.Predicate.Satisfied = nil
		} else {
			report.Frame.AgeMS = float64(time.Since(checkedAt)) / float64(time.Millisecond)
		}
	}
	report.Observation.DurationMS = float64(time.Since(started)) / float64(time.Millisecond)
	encoded, err := json.Marshal(report)
	if err != nil {
		return nil, nil, err
	}
	result := textResult(string(encoded))
	result.StructuredContent = report
	if report.Frame != nil {
		result.Content = append(result.Content, &mcp.ImageContent{Data: checked.jpeg, MIMEType: "image/jpeg"})
	}
	return result, nil, nil
}

func visualChange(previous, current []byte) bool {
	if len(previous) != len(current) {
		return true
	}
	for index, value := range current {
		difference := int(value) - int(previous[index])
		if difference > 12 || difference < -12 {
			return true
		}
	}
	return false
}
