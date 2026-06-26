//go:build linux

package main

import (
	"slices"
	"testing"
	"time"
)

func TestInputDeviceMatchesID(t *testing.T) {
	dev := inputDeviceInfo{
		Bus:     "0005",
		Vendor:  "045e",
		Product: "0b22",
		Version: "0521",
		Uniq:    "f4:6a:d7:1f:1c:ac",
	}

	for _, id := range []string{
		"vendor=045e product=0b22",
		"vendor=045e product=0b22 uniq=f4:6a:d7:1f:1c:ac",
		"bus=0005 vendor=045e product=0b22 version=0521 uniq=f4:6a:d7:1f:1c:ac",
	} {
		if !dev.matchesID(id) {
			t.Fatalf("matchesID(%q): got false, want true", id)
		}
	}

	if dev.matchesID("vendor=045e product=ffff") {
		t.Fatal("matchesID with the wrong product: got true, want false")
	}
}

func TestParseChordSupportsButtonsDpadAliasesAndRawAbsInputs(t *testing.T) {
	inputs, err := parseChord([]string{"BTN_TL", "DPAD_UP", "ABS_HAT0X:-1", "abs:ABS_HAT0Y:1"})
	if err != nil {
		t.Fatalf("parseChord: %v", err)
	}

	want := []chordInput{
		{Type: evKey, Code: keyCodes["BTN_TL"], Value: 1},
		{Type: evAbs, Code: 0x10, Value: -1},
		{Type: evAbs, Code: 0x11, Value: -1},
		{Type: evAbs, Code: 0x11, Value: 1},
	}
	for _, input := range want {
		if !slices.Contains(inputs, input) {
			t.Fatalf("parseChord missing %#v in %#v", input, inputs)
		}
	}
}

func TestUpdatePressedInputsTracksDpadDirections(t *testing.T) {
	pressed := make(map[chordInput]bool)
	up := dpadInputs["DPAD_UP"]
	down := dpadInputs["DPAD_DOWN"]

	if !updatePressedInputs(pressed, inputEvent{Type: evAbs, Code: 0x11, Value: -1}) {
		t.Fatal("dpad up event: got unchanged, want changed")
	}
	if !pressed[up] || pressed[down] {
		t.Fatalf("dpad up state: got %#v", pressed)
	}

	updatePressedInputs(pressed, inputEvent{Type: evAbs, Code: 0x11, Value: 1})
	if pressed[up] || !pressed[down] {
		t.Fatalf("dpad down state: got %#v", pressed)
	}

	updatePressedInputs(pressed, inputEvent{Type: evAbs, Code: 0x11, Value: 0})
	if pressed[up] || pressed[down] {
		t.Fatalf("dpad neutral state: got %#v", pressed)
	}
}

func TestInputDebouncerPrintsAnalogStickDirectionChanges(t *testing.T) {
	debouncer := testInputDebouncer{
		axes: map[uint16]analogAxis{
			0x00: {center: 128, deadzone: 20, negativeName: "left", positiveName: "right"},
		},
	}
	now := time.Unix(100, 0)

	if shouldPrint, _ := debouncer.shouldPrint(inputEvent{Type: evAbs, Code: 0x00, Value: 130}, now); shouldPrint {
		t.Fatal("initial neutral analog stick event: got true, want false")
	}
	shouldPrint, direction := debouncer.shouldPrint(inputEvent{Type: evAbs, Code: 0x00, Value: 210}, now)
	if !shouldPrint || direction != "right" {
		t.Fatalf("right analog stick event: got (%v, %q), want (true, right)", shouldPrint, direction)
	}
	if shouldPrint, _ := debouncer.shouldPrint(inputEvent{Type: evAbs, Code: 0x00, Value: 250}, now.Add(testInputAnalogDebounce)); shouldPrint {
		t.Fatal("same-direction analog stick event: got true, want false")
	}
	shouldPrint, direction = debouncer.shouldPrint(inputEvent{Type: evAbs, Code: 0x00, Value: 128}, now.Add(testInputAnalogDebounce+time.Millisecond))
	if !shouldPrint || direction != "neutral" {
		t.Fatalf("neutral analog stick event: got (%v, %q), want (true, neutral)", shouldPrint, direction)
	}
}

func TestInputDebouncerDoesNotDebounceButtonsTriggersOrHats(t *testing.T) {
	debouncer := testInputDebouncer{}
	now := time.Unix(100, 0)
	events := []inputEvent{
		{Type: evKey, Code: keyCodes["BTN_SOUTH"], Value: 1},
		{Type: evAbs, Code: 0x02, Value: 255},
		{Type: evAbs, Code: 0x10, Value: 1},
	}

	for _, event := range events {
		if shouldPrint, _ := debouncer.shouldPrint(event, now); !shouldPrint {
			t.Fatalf("first event %#v: got false, want true", event)
		}
		if shouldPrint, _ := debouncer.shouldPrint(event, now.Add(time.Millisecond)); !shouldPrint {
			t.Fatalf("rapid event %#v: got false, want true", event)
		}
	}
}

func TestAnalogCenterAndDeadzoneUsesRangeAndFlat(t *testing.T) {
	center, deadzone := analogCenterAndDeadzone(0, 255, 4)
	if center != 127 || deadzone != 63 {
		t.Fatalf("0..255 axis: got center=%d deadzone=%d, want center=127 deadzone=63", center, deadzone)
	}

	center, deadzone = analogCenterAndDeadzone(-32768, 32767, 9000)
	if center != -1 || deadzone != 16383 {
		t.Fatalf("-32768..32767 axis: got center=%d deadzone=%d, want center=-1 deadzone=16383", center, deadzone)
	}
}
