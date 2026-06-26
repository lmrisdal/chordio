//go:build linux

package main

import (
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

func TestInputDebouncerDebouncesAnalogStickAxes(t *testing.T) {
	debouncer := testInputDebouncer{}
	event := inputEvent{Type: evAbs, Code: 0x00, Value: 123}
	now := time.Unix(100, 0)

	if !debouncer.shouldPrint(event, now) {
		t.Fatal("first analog stick event: got false, want true")
	}
	if debouncer.shouldPrint(event, now.Add(testInputAnalogDebounce-time.Millisecond)) {
		t.Fatal("rapid analog stick event: got true, want false")
	}
	if !debouncer.shouldPrint(event, now.Add(testInputAnalogDebounce)) {
		t.Fatal("debounced analog stick event: got false, want true")
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
		if !debouncer.shouldPrint(event, now) {
			t.Fatalf("first event %#v: got false, want true", event)
		}
		if !debouncer.shouldPrint(event, now.Add(time.Millisecond)) {
			t.Fatalf("rapid event %#v: got false, want true", event)
		}
	}
}
