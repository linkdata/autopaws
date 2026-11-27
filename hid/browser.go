package hid

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

// Browser holds one connection's physical keyboard state. Its ordered caller
// owns it; methods must not run concurrently. Complete actions share the same
// Controller as API keyboard/mouse actions. Concurrent operators are unsupported.
type Browser struct {
	controller      *Controller
	mouse           *Mouse
	generation      uint64
	prepared        bool
	modifier        byte
	keys            []byte
	buttons         byte
	mappingRevision uint64
}

func NewBrowser(controller *Controller, mouse *Mouse) *Browser {
	if controller == nil || mouse == nil || mouse.controller != controller {
		panic("hid: browser requires one shared controller and mouse")
	}
	return &Browser{controller: controller, mouse: mouse}
}

func (browser *Browser) clear() {
	browser.modifier, browser.keys, browser.buttons = 0, nil, 0
}

func (browser *Browser) reconcile() {
	if !browser.prepared || browser.generation != browser.controller.generation {
		browser.clear()
	}
	browser.prepared, browser.generation = true, browser.controller.generation
	if revision := browser.mouse.Mapping().Revision; revision != browser.mappingRevision {
		browser.clear()
		browser.mappingRevision = revision
	}
}

func (browser *Browser) run(ctx context.Context, execute func(*action) error) error {
	var admitted *action
	err := browser.controller.run(ctx, releaseBoth, func(current *action) error {
		admitted = current
		browser.reconcile()
		return execute(current)
	})
	if err != nil && admitted != nil && admitted.dirty {
		// run already attempted one release-only cleanup for both input classes.
		// A disconnect must not start a second cleanup budget for that failure.
		browser.clear()
	}
	return err
}

func (browser *Browser) keyboard(current *action, modifier byte, keys []byte) error {
	if err := current.write(ch9329.Keyboard(modifier, keys...)); err != nil {
		return err
	}
	browser.modifier, browser.keys = modifier, keys
	return nil
}

// Key uses KeyboardEvent.code, independent of the selected text layout. Unknown
// codes are ignored. Non-modifiers stay in press order; a seventh is ignored.
func (browser *Browser) Key(ctx context.Context, code string, down bool) error {
	modifier, isModifier := browserModifiers[code]
	key, isKey := browserKeys[code]
	if !isModifier && !isKey {
		return ctx.Err()
	}
	return browser.run(ctx, func(current *action) error {
		modifiers, keys := browser.modifier, slices.Clone(browser.keys)
		if isModifier {
			if down {
				modifiers |= modifier
			} else {
				modifiers &^= modifier
			}
		} else {
			index := slices.Index(keys, key)
			if down {
				if index < 0 {
					if len(keys) == 6 {
						return nil
					}
					keys = append(keys, key)
				}
			} else if index >= 0 {
				keys = slices.Delete(keys, index, index+1)
			}
		}
		return browser.keyboard(current, modifiers, keys)
	})
}

func normalizedPoint(x, y int) (Point, error) {
	if x < 0 || x > 4095 || y < 0 || y > 4095 {
		return Point{}, fmt.Errorf("%w: normalized (%d,%d)", ErrCoordinates, x, y)
	}
	return Point{X: x, Y: y}, nil
}

// Move accepts HID-normalized coordinates directly and preserves actual held
// buttons. Going through target pixels here would introduce double rounding.
func (browser *Browser) Move(ctx context.Context, x, y int) error {
	point, err := normalizedPoint(x, y)
	if err != nil {
		return browser.mappingFailure(ctx, err)
	}
	points, revision, err := browser.mouse.prepareMapping(ctx, point)
	if err != nil {
		return browser.mappingFailure(ctx, err)
	}
	return browser.runMapped(ctx, revision, func(current *action) error { return browser.mouse.moveAbs(current, points[0]) })
}

// Buttons applies the complete left/right/middle mask at normalized coordinates.
// Explicit button events are authoritative; movement alone does not change it.
func (browser *Browser) Buttons(ctx context.Context, x, y, buttons int) error {
	point, err := normalizedPoint(x, y)
	if err != nil {
		return browser.mappingFailure(ctx, err)
	}
	if buttons < 0 || buttons > 7 {
		return browser.mappingFailure(ctx, fmt.Errorf("%w: mask %d", ErrButton, buttons))
	}
	points, revision, err := browser.mouse.prepareMapping(ctx, point)
	if err != nil {
		return browser.mappingFailure(ctx, err)
	}
	return browser.runMapped(ctx, revision, func(current *action) error {
		if err := browser.mouse.setButtons(current, byte(buttons), buttonPlan{relative: browser.mouse.relative, positioned: true, position: points[0]}); err != nil {
			return err
		}
		browser.buttons = byte(buttons)
		return nil
	})
}

func (browser *Browser) mappingFailure(ctx context.Context, err error) error {
	if browser.buttons != 0 {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		return errors.Join(err, browser.Release(cleanup))
	}
	return err
}

func (browser *Browser) runMapped(ctx context.Context, revision uint64, execute func(*action) error) error {
	var admitted *action
	err := browser.mouse.runMappedMask(ctx, revision, releaseBoth, func(current *action) error {
		admitted = current
		browser.reconcile()
		return execute(current)
	})
	if err != nil {
		if admitted != nil && admitted.dirty {
			browser.clear()
			return err
		}
		return browser.mappingFailure(ctx, err)
	}
	return nil
}

func (browser *Browser) Scroll(ctx context.Context, amount int) error {
	return browser.run(ctx, func(current *action) error {
		return current.write(ch9329.MouseRelative(browser.controller.relativeButtons, 0, 0, amount))
	})
}

// Shortcut presses a validated physical-key chord for 10ms and restores prior
// held keys as one action. Existing held modifiers/keys remain held afterwards.
func (browser *Browser) Shortcut(ctx context.Context, codes []string) error {
	if len(codes) == 0 {
		return fmt.Errorf("%w: empty browser shortcut", ErrKeycode)
	}
	var modifiers byte
	var keys []byte
	for _, code := range codes {
		if modifier, ok := browserModifiers[code]; ok {
			modifiers |= modifier
		} else if key, ok := browserKeys[code]; ok {
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		} else {
			return fmt.Errorf("%w: browser code %q", ErrKeycode, code)
		}
	}
	if len(keys) > 6 {
		return fmt.Errorf("%w: browser shortcut exceeds six keys", ErrKeycode)
	}
	return browser.run(ctx, func(current *action) error {
		previousModifier, previousKeys := browser.modifier, slices.Clone(browser.keys)
		combined := slices.Clone(previousKeys)
		for _, key := range keys {
			if !slices.Contains(combined, key) {
				combined = append(combined, key)
			}
		}
		if len(combined) > 6 {
			return fmt.Errorf("%w: browser shortcut exceeds six held keys", ErrKeycode)
		}
		if err := browser.keyboard(current, previousModifier|modifiers, combined); err != nil {
			return err
		}
		if err := wait(current.ctx, 10*time.Millisecond); err != nil {
			return err
		}
		return browser.keyboard(current, previousModifier, previousKeys)
	})
}

// Release sends only this connection's owned release reports. A passive
// connection does not prepare/open hardware. The caller supplies an uncancelled
// one-second cleanup context on disconnect. Failed releases are not retried here:
// the transport invalidation and next Prepare reset reconcile them.
func (browser *Browser) Release(ctx context.Context) error {
	if browser.modifier == 0 && len(browser.keys) == 0 && browser.buttons == 0 {
		return nil
	}
	var release releaseMask
	if browser.modifier != 0 || len(browser.keys) != 0 {
		release |= releaseKeyboard
	}
	if browser.buttons != 0 {
		release |= releaseMouse
	}
	attempted, err := browser.controller.releaseHeld(ctx, release)
	if attempted {
		browser.clear()
	}
	return err
}
