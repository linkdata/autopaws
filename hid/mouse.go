package hid

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

var (
	ErrScreenSize  = errors.New("invalid target screen size")
	ErrCoordinates = errors.New("mouse coordinates out of range")
	ErrButton      = errors.New("unknown mouse button")
	ErrButtonMode  = errors.New("unknown mouse button mode")
	ErrMovement    = errors.New("relative mouse movement out of range")
)

// Point is a zero-based target-screen pixel position.
type Point struct{ X, Y int }

// MouseOptions defines the target size and default button-report mode.
// ButtonMode is "relative" (also the empty default) or "absolute".
type MouseOptions struct {
	Width, Height int
	ButtonMode    string
}

// ButtonOptions optionally positions a button action and overrides its mode.
// Empty Mode uses the configured default. The point is copied before admission.
type ButtonOptions struct {
	Position *Point
	Mode     string
}

// Mouse shares its controller's held input and complete-action gate.
type Mouse struct {
	controller    *Controller
	width, height int
	relative      bool
}

// NewMouse validates target dimensions and mode without opening hardware.
// A nil controller is a programming error and panics.
func NewMouse(controller *Controller, options MouseOptions) (*Mouse, error) {
	if controller == nil {
		panic("hid: nil mouse controller")
	}
	if options.Width <= 0 || options.Height <= 0 {
		return nil, fmt.Errorf("%w: %dx%d", ErrScreenSize, options.Width, options.Height)
	}
	relative, err := buttonMode(options.ButtonMode)
	if err != nil {
		return nil, err
	}
	return &Mouse{controller: controller, width: options.Width, height: options.Height, relative: relative}, nil
}

func buttonMode(mode string) (bool, error) {
	switch mode {
	case "", "relative":
		return true, nil
	case "absolute":
		return false, nil
	default:
		return false, fmt.Errorf("%w: %q", ErrButtonMode, mode)
	}
}

func (mouse *Mouse) position(point Point) (Point, error) {
	if point.X < 0 || point.X >= mouse.width || point.Y < 0 || point.Y >= mouse.height {
		return Point{}, fmt.Errorf("%w: (%d,%d) in %dx%d", ErrCoordinates, point.X, point.Y, mouse.width, mouse.height)
	}
	return Point{pixelToHID(point.X, mouse.width), pixelToHID(point.Y, mouse.height)}, nil
}

func pixelToHID(pixel, dimension int) int {
	if dimension == 1 {
		return 0
	}
	denominator := uint64(dimension - 1)
	high, low := bits.Mul64(uint64(pixel), 4095)
	low, carry := bits.Add64(low, denominator/2, 0)
	value, _ := bits.Div64(high+carry, low, denominator)
	return int(value)
}

type buttonPlan struct {
	bit        byte
	position   Point
	positioned bool
	relative   bool
}

func (mouse *Mouse) planButton(button string, options ButtonOptions) (buttonPlan, error) {
	plan := buttonPlan{relative: mouse.relative}
	switch button {
	case "", "left":
		plan.bit = 1
	case "right":
		plan.bit = 2
	case "middle":
		plan.bit = 4
	default:
		return plan, fmt.Errorf("%w: %q", ErrButton, button)
	}
	if options.Mode != "" {
		var err error
		plan.relative, err = buttonMode(options.Mode)
		if err != nil {
			return plan, err
		}
	}
	if options.Position != nil {
		var err error
		plan.position, err = mouse.position(*options.Position)
		if err != nil {
			return plan, err
		}
		plan.positioned = true
	}
	return plan, nil
}

// MoveAbs moves to a target-screen pixel, preserving held buttons.
func (mouse *Mouse) MoveAbs(ctx context.Context, x, y int) error {
	position, err := mouse.position(Point{x, y})
	if err != nil {
		return err
	}
	return mouse.controller.run(ctx, releaseMouse, func(current *action) error {
		return mouse.moveAbs(current, position)
	})
}

func (mouse *Mouse) moveAbs(current *action, position Point) error {
	return current.write(ch9329.MouseAbsolute(mouse.controller.buttons, position.X, position.Y, 0))
}

// MoveRel moves by -127..127 on each axis, preserving held buttons.
func (mouse *Mouse) MoveRel(ctx context.Context, dx, dy int) error {
	if dx < -127 || dx > 127 || dy < -127 || dy > 127 {
		return fmt.Errorf("%w: (%d,%d)", ErrMovement, dx, dy)
	}
	return mouse.controller.run(ctx, releaseMouse, func(current *action) error {
		return current.write(ch9329.MouseRelative(mouse.controller.buttons, dx, dy, 0))
	})
}

// Scroll preserves held buttons and clamps wheel movement to -127..127.
func (mouse *Mouse) Scroll(ctx context.Context, amount int) error {
	return mouse.controller.run(ctx, releaseMouse, func(current *action) error {
		return current.write(ch9329.MouseRelative(mouse.controller.buttons, 0, 0, amount))
	})
}

func (mouse *Mouse) setButtons(current *action, buttons byte, plan buttonPlan) error {
	if plan.positioned {
		absoluteButtons := buttons
		if plan.relative {
			absoluteButtons = 0
		}
		if err := current.write(ch9329.MouseAbsolute(absoluteButtons, plan.position.X, plan.position.Y, 0)); err != nil {
			return err
		}
		if !plan.relative {
			mouse.controller.buttons = buttons
			return nil
		}
	}
	if err := current.write(ch9329.MouseRelative(buttons, 0, 0, 0)); err != nil {
		return err
	}
	mouse.controller.buttons = buttons
	return nil
}

// Down holds button, optionally moving first. Empty button means left.
func (mouse *Mouse) Down(ctx context.Context, button string, options ButtonOptions) error {
	plan, err := mouse.planButton(button, options)
	if err != nil {
		return err
	}
	return mouse.controller.run(ctx, releaseMouse, func(current *action) error {
		return mouse.setButtons(current, mouse.controller.buttons|plan.bit, plan)
	})
}

// Up releases only button, preserving any other held buttons.
func (mouse *Mouse) Up(ctx context.Context, button string, options ButtonOptions) error {
	plan, err := mouse.planButton(button, options)
	if err != nil {
		return err
	}
	return mouse.controller.run(ctx, releaseMouse, func(current *action) error {
		return mouse.setButtons(current, mouse.controller.buttons&^plan.bit, plan)
	})
}

// Click presses, holds for 10ms and releases button as one action. Other held
// buttons are preserved. Clicking an already held button ends with it released.
func (mouse *Mouse) Click(ctx context.Context, button string, options ButtonOptions) error {
	plan, err := mouse.planButton(button, options)
	if err != nil {
		return err
	}
	return mouse.controller.run(ctx, releaseMouse, func(current *action) error {
		if err := mouse.setButtons(current, mouse.controller.buttons|plan.bit, plan); err != nil {
			return err
		}
		if err := wait(current.ctx, 10*time.Millisecond); err != nil {
			return err
		}
		return mouse.setButtons(current, mouse.controller.buttons&^plan.bit, plan)
	})
}

// Drag holds button from start to end with 50ms pauses before and after the move.
// The complete request is validated before preparing or emitting input.
func (mouse *Mouse) Drag(ctx context.Context, start, end Point, button, mode string) error {
	plan, err := mouse.planButton(button, ButtonOptions{Position: &start, Mode: mode})
	if err != nil {
		return err
	}
	endPosition, err := mouse.position(end)
	if err != nil {
		return err
	}
	return mouse.controller.run(ctx, releaseMouse, func(current *action) error {
		if err := mouse.setButtons(current, mouse.controller.buttons|plan.bit, plan); err != nil {
			return err
		}
		if err := wait(current.ctx, 50*time.Millisecond); err != nil {
			return err
		}
		if err := mouse.moveAbs(current, endPosition); err != nil {
			return err
		}
		if err := wait(current.ctx, 50*time.Millisecond); err != nil {
			return err
		}
		plan.position = endPosition
		return mouse.setButtons(current, mouse.controller.buttons&^plan.bit, plan)
	})
}
