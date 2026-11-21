"""Minimal flat GTK4 floating HUD capsule for voice-type."""

from __future__ import annotations

import json
import sys
import threading

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Gdk", "4.0")
import cairo  # noqa: E402
from gi.repository import Gdk, GLib, Gtk  # noqa: E402

APP_ID = "io.github.voicetype.hud"

_CSS = """
window,
window.background,
window.voice-hud-window {
    background: none;
    background-color: transparent;
    border: none;
    box-shadow: none;
    outline: none;
}
.voice-hud-pill {
    margin: 0;
    background-color: rgba(28, 28, 34, 0.68);
    border: none;
    box-shadow: none;
    border-radius: 18px;
    padding: 6px 14px;
}
.voice-hud-dot {
    min-width: 8px;
    min-height: 8px;
    border-radius: 4px;
}
.voice-hud-dot.listening {
    background-color: #34d399;
}
.voice-hud-dot.processing {
    background-color: #60a5fa;
}
.voice-hud-label {
    color: #f4f4f5;
    font-size: 13px;
    font-weight: 500;
}
"""


class HudApp(Gtk.Application):
    """GTK4 resident capsule window."""

    def __init__(self) -> None:
        super().__init__(application_id=APP_ID)
        self._window: Gtk.Window | None = None
        self._dot: Gtk.Box | None = None
        self._label: Gtk.Label | None = None

    def do_activate(self) -> None:
        if self._window is not None:
            return

        provider = Gtk.CssProvider()
        provider.load_from_data(_CSS.encode("utf-8"))
        display = Gdk.Display.get_default()
        if display is not None:
            Gtk.StyleContext.add_provider_for_display(
                display, provider, Gtk.STYLE_PROVIDER_PRIORITY_USER
            )

        win = Gtk.Window(application=self)
        win.set_title("voice-type")
        win.set_decorated(False)
        win.set_resizable(False)
        win.set_can_focus(False)
        win.set_focusable(False)
        win.add_css_class("voice-hud-window")
        win.connect("map", self._make_click_through)

        pill = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        pill.add_css_class("voice-hud-pill")
        pill.set_valign(Gtk.Align.CENTER)
        pill.set_halign(Gtk.Align.CENTER)

        dot = Gtk.Box()
        dot.add_css_class("voice-hud-dot")
        dot.add_css_class("listening")
        dot.set_valign(Gtk.Align.CENTER)

        label = Gtk.Label(label="正在聆听...")
        label.add_css_class("voice-hud-label")

        pill.append(dot)
        pill.append(label)
        win.set_child(pill)

        # Keep application alive even when window is hidden
        self.hold()
        win.set_visible(False)

        self._window = win
        self._dot = dot
        self._label = label

        threading.Thread(target=self._read_stdin, daemon=True).start()

    @staticmethod
    def _make_click_through(win: Gtk.Window) -> None:
        surface = win.get_surface()
        if surface is not None:
            surface.set_input_region(cairo.Region())

    def _apply_command(self, cmd: dict[str, str]) -> bool:
        action = cmd.get("action", "")
        if action == "quit":
            self.quit()
            return False

        if self._window is None or self._dot is None or self._label is None:
            return False

        if action == "hide":
            self._window.set_visible(False)
            return False

        if action == "show":
            state = cmd.get("state", "listening")
            text = cmd.get("text", "正在聆听...")
            self._dot.remove_css_class("listening")
            self._dot.remove_css_class("processing")
            self._dot.add_css_class("processing" if state == "processing" else "listening")
            self._label.set_text(text)
            self._window.set_visible(True)
        return False

    def _read_stdin(self) -> None:
        for line in sys.stdin:
            line = line.strip()
            if not line:
                continue
            try:
                cmd = json.loads(line)
            except json.JSONDecodeError:
                continue
            GLib.idle_add(self._apply_command, cmd)
        GLib.idle_add(self.quit)


def main() -> int:
    app = HudApp()
    return app.run(None)


if __name__ == "__main__":
    sys.exit(main())
