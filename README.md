# Chordio

Chordio is a Linux gamepad chord launcher. Hold a button combination on a
controller, and Chordio runs the action you configured.

Examples:

- raise or lower volume with `wpctl`
- toggle audio mute with `wpctl`
- run a script
- start or restart a systemd unit

Chordio reads Linux input events directly, so it works independently of X11,
Wayland, Gamescope, and which window has focus.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/lmrisdal/chordio/main/packaging/install.sh | sh
```

The installer places:

- binary: `/usr/local/bin/chordio`
- config: `/etc/chordio/config.json`
- optional service: `/etc/systemd/system/chordio.service`

After installing:

```sh
chordio --list-devices
sudo nano /etc/chordio/config.json
chordio --config /etc/chordio/config.json --debug
sudo systemctl start chordio.service
```

## Update

```sh
sudo chordio update
```

Use `--force` to reinstall the latest release even when the version already
matches.

## Uninstall

```sh
sudo chordio uninstall
```

Chordio will show installed components, then let you remove the systemd service
or everything, including the binary and config.

## Find Your Controller

Run:

```sh
chordio --list-devices
```

Chordio prints likely gamepads, whether they are readable, and stable device
paths such as:

```text
/dev/input/by-id/usb-Microsoft_X-Box_Controller-event-joystick
```

Copy a stable controller path into your config:

```json
{
  "devices": ["/dev/input/by-id/usb-Microsoft_X-Box_Controller-event-joystick"]
}
```

If your controller is not listed, run:

```sh
chordio --list-devices --list-all-devices
```

Prefer controller devices with `event-joystick` in the path. Do not choose
keyboard-like devices.

To see what your controller reports when you press buttons or move triggers,
run:

```sh
chordio test-input
```

Chordio will use configured devices when it can find a config file, otherwise it
will listen to likely gamepads. You can also point it at a specific device:

```sh
chordio test-input --device /dev/input/by-id/usb-Microsoft_X-Box_Controller-event-joystick
```

The output includes the event type, evdev code name, numeric code, and value.
For example, `A` usually appears as `BTN_SOUTH(304)`. Triggers may appear as
button codes such as `BTN_TL2`, or as axis codes such as `ABS_Z`, depending on
the controller and driver.

## Configure Chords

Example config:

```json
{
  "scan_interval_sec": 2,
  "devices": ["/dev/input/by-id/usb-Microsoft_X-Box_Controller-event-joystick"],
  "chords": [
    {
      "name": "volume-up",
      "chord": ["BTN_THUMBL", "BTN_TR", "BTN_WEST"],
      "cooldown_ms": 1000,
      "action": {
        "type": "exec",
        "command": ["wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", "5%+"]
      }
    }
  ]
}
```

Top-level fields:

| Field               | Meaning                                                                                                            |
| ------------------- | ------------------------------------------------------------------------------------------------------------------ |
| `devices`           | Controller input devices Chordio may read. Prefer stable `/dev/input/by-id/...` or `/dev/input/by-path/...` paths. |
| `scan_interval_sec` | How often Chordio checks for newly connected configured devices.                                                   |
| `chords`            | Chord definitions.                                                                                                 |

Chord fields:

| Field         | Meaning                                          |
| ------------- | ------------------------------------------------ |
| `name`        | Log-friendly chord name.                         |
| `chord`       | Button names that must be held at the same time. |
| `inputs`      | Alias for `chord`.                               |
| `enabled`     | Set `false` to keep an example disabled.         |
| `cooldown_ms` | Suppress repeats within this window.             |
| `action`      | Action to run when the chord is pressed.         |

Common Xbox-style button names:

| Button      | evdev key    |
| ----------- | ------------ |
| A           | `BTN_SOUTH`  |
| B           | `BTN_EAST`   |
| X           | `BTN_WEST`   |
| Y           | `BTN_NORTH`  |
| LB          | `BTN_TL`     |
| RB          | `BTN_TR`     |
| LT          | `BTN_TL2`    |
| RT          | `BTN_TR2`    |
| Select/View | `BTN_SELECT` |
| Start/Menu  | `BTN_START`  |
| Guide       | `BTN_MODE`   |
| L3          | `BTN_THUMBL` |
| R3          | `BTN_THUMBR` |

## Actions

### exec

Run a command directly:

```json
{
  "type": "exec",
  "command": ["wpctl", "set-mute", "@DEFAULT_AUDIO_SINK@", "toggle"]
}
```

### shell

Run a shell snippet with `/bin/sh -c`:

```json
{
  "type": "shell",
  "shell": "notify-send Chordio 'Chord fired'"
}
```

Prefer `exec` for normal use. Use `shell` only when you specifically need shell
features such as pipes or redirects.

### script

Run an executable script path with optional args:

```json
{
  "type": "script",
  "path": "/home/USER/.local/share/chordio/scripts/do-something.sh",
  "args": ["one", "two"]
}
```

### systemd

Run `systemctl` for a unit:

```json
{
  "type": "systemd",
  "op": "restart",
  "unit": "some-service.service"
}
```

Set `"user": true` to call `systemctl --user`.

## Security

Chordio needs permission to read the controller devices listed in its config.
Linux input devices can include keyboards and mice, so choose controller devices
only. Avoid adding your user to the broad `input` group unless you understand
the implications.

Keep `/etc/chordio/config.json` writable only by trusted users, because actions
can run commands and scripts.

## Debugging

Run interactively first:

```sh
chordio --config /etc/chordio/config.json --debug
```

Press buttons one at a time and watch the reported button names. If Chordio
cannot read your controller, adjust permissions for that specific device or
udev rule rather than granting access to every input device.

## Build It Yourself

```sh
go build -o chordio .
sudo install -Dm755 chordio /usr/local/bin/chordio
sudo install -Dm644 packaging/config.example.json /etc/chordio/config.json
sudo install -Dm644 packaging/chordio.service /etc/systemd/system/chordio.service
sudo systemctl daemon-reload
sudo systemctl enable chordio.service
```
