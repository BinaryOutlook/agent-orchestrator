import { describe, expect, it, vi } from "vitest";
import { CLOSE_SHELL_TERMINAL_SHORTCUT_CHANNEL, APP_SHORTCUT_CHANNEL, KEYBOARD_SHORTCUTS_HELP_CHANNEL, NEXT_SESSION_SHORTCUT_CHANNEL, NEXT_TAB_SHORTCUT_CHANNEL, NEW_SESSION_SHORTCUT_CHANNEL, NEW_SHELL_TERMINAL_SHORTCUT_CHANNEL, OPEN_SETTINGS_SHORTCUT_CHANNEL, PREVIOUS_SESSION_SHORTCUT_CHANNEL, PREVIOUS_TAB_SHORTCUT_CHANNEL, TERMINAL_FONT_SIZE_SHORTCUT_CHANNEL } from "../shared/shortcuts";
import { attachAppShortcuts } from "./app-shortcuts";
import { toggleAppDevTools } from "./app-devtools";

type InputEvent = {
	key: string;
	// Physical key (Electron input.code), needed for chords whose character is
	// layout-shifted, e.g. Ctrl+Shift+` reports key "~" but code "Backquote".
	code?: string;
	control: boolean;
	meta: boolean;
	shift: boolean;
	alt: boolean;
	type: "keyDown" | "keyUp";
	isAutoRepeat?: boolean;
};

function fakeSource() {
	let handler: ((event: { preventDefault: () => void }, input: InputEvent) => void) | undefined;
	return {
		on(channel: string, listener: typeof handler) {
			if (channel === "before-input-event") handler = listener;
			return this;
		},
		emit(input: Partial<InputEvent> & { key: string }) {
			const event = { preventDefault: vi.fn() };
			handler?.(event, {
				control: false,
				meta: false,
				shift: false,
				alt: false,
				type: "keyDown",
				...input,
			});
			return event;
		},
	};
}

function fakeTarget() {
	return { focus: vi.fn(), send: vi.fn() };
}

describe("attachAppShortcuts", () => {
	it("routes Ctrl+Shift+I to shell DevTools when no Browser view is available", async () => {
		const source = fakeSource();
		const target = { ...fakeTarget(), toggleDevTools: vi.fn() };
		const browserHost = { toggleDevToolsForLastFocused: vi.fn().mockResolvedValue(null) };
		let toggle: Promise<void> | undefined;
		attachAppShortcuts(source, false, target, false, () => ({}), () => false, () => true, (id) => {
			if (id === "toggle-browser-devtools") toggle = toggleAppDevTools(browserHost, () => target);
		});

		const event = source.emit({ key: "I", control: true, shift: true });
		source.emit({ key: "I", control: true, shift: true, type: "keyUp" });
		await toggle;

		expect(event.preventDefault).toHaveBeenCalledOnce();
		expect(target.toggleDevTools).toHaveBeenCalledOnce();
		expect(target.send).not.toHaveBeenCalled();
	});

	it("forwards and prevents default on the main-window chord", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target);

		const event = source.emit({ key: "N", control: true, shift: true });

		expect(target.send).toHaveBeenCalledWith(NEW_SESSION_SHORTCUT_CHANNEL);
		expect(target.focus).not.toHaveBeenCalled();
		expect(event.preventDefault).toHaveBeenCalledTimes(1);
	});

	it("forwards the macOS command chord", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, true, target);

		source.emit({ key: "n", meta: true });

		expect(target.send).toHaveBeenCalledWith(NEW_SESSION_SHORTCUT_CHANNEL);
	});

	it("focuses a separate shell target before forwarding", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target, true);

		source.emit({ key: "N", control: true, shift: true });

		expect(target.focus).toHaveBeenCalledTimes(1);
		expect(target.send).toHaveBeenCalledWith(NEW_SESSION_SHORTCUT_CHANNEL);
		expect(target.focus.mock.invocationCallOrder[0]).toBeLessThan(target.send.mock.invocationCallOrder[0]);
	});

	it("ignores non-matching chords and key-up events", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target);

		source.emit({ key: "n", control: true });
		source.emit({ key: "N", control: true, shift: true, type: "keyUp" });
		source.emit({ key: "x", control: true, shift: true });

		expect(target.send).not.toHaveBeenCalled();
	});

	it("ignores auto-repeat so holding the combo fires once", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target);

		source.emit({ key: "N", control: true, shift: true });
		source.emit({ key: "N", control: true, shift: true, isAutoRepeat: true });
		source.emit({ key: "N", control: true, shift: true, isAutoRepeat: true });

		expect(target.send).toHaveBeenCalledTimes(1);
	});

	it.each([
		["macOS", true, { key: "t", meta: true }],
		["Windows/Linux", false, { key: "t", control: true }],
	])("forwards the new-shell-terminal chord on %s", (_name, isMac, input) => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, isMac, target);

		source.emit(input);

		expect(target.send).toHaveBeenCalledWith(NEW_SHELL_TERMINAL_SHORTCUT_CHANNEL);
	});

	it.each([
		["macOS", true, { key: "w", meta: true }],
		["Windows/Linux", false, { key: "w", control: true }],
	])("forwards and consumes the close-shell-terminal chord on %s", (_name, isMac, input) => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, isMac, target);

		const event = source.emit(input);

		expect(target.send).toHaveBeenCalledWith(CLOSE_SHELL_TERMINAL_SHORTCUT_CHANNEL);
		expect(event.preventDefault).toHaveBeenCalledOnce();
	});

	it("consumes auto-repeat chords without re-firing so held ⌘W cannot reach menu Close", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target);

		const handledRepeat = source.emit({ key: "w", control: true, isAutoRepeat: true });
		expect(handledRepeat.preventDefault).toHaveBeenCalledOnce();
		expect(target.send).not.toHaveBeenCalled();

		const rejectedSource = fakeSource();
		const rejectedTarget = fakeTarget();
		attachAppShortcuts(rejectedSource, false, rejectedTarget, false, () => ({}), () => false, (id) => id !== "close-shell-terminal");
		const rejectedRepeat = rejectedSource.emit({ key: "w", control: true, isAutoRepeat: true });
		expect(rejectedRepeat.preventDefault).toHaveBeenCalledOnce();
		expect(rejectedTarget.send).not.toHaveBeenCalled();
	});

	it("consumes terminal tab chords when a browser context rejects them", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target, false, () => ({}), () => false, (id) => id !== "close-shell-terminal");

		const event = source.emit({ key: "w", control: true });

		expect(target.send).not.toHaveBeenCalled();
		// Still preventDefault so a racing listener cannot open/close a terminal,
		// and so Chromium does not treat the chord as an unhandled accelerator.
		expect(event.preventDefault).toHaveBeenCalledOnce();
	});

	it("forwards keyboard-shortcut help on each platform", () => {
		const windowsSource = fakeSource();
		const windowsTarget = fakeTarget();
		attachAppShortcuts(windowsSource, false, windowsTarget);
		windowsSource.emit({ key: "/", control: true });

		const macSource = fakeSource();
		const macTarget = fakeTarget();
		attachAppShortcuts(macSource, true, macTarget);
		macSource.emit({ key: "/", meta: true });

		expect(windowsTarget.send).toHaveBeenCalledWith(KEYBOARD_SHORTCUTS_HELP_CHANNEL);
		expect(macTarget.send).toHaveBeenCalledWith(KEYBOARD_SHORTCUTS_HELP_CHANNEL);
	});

	it.each([
		["settings", { key: ",", control: true }, OPEN_SETTINGS_SHORTCUT_CHANNEL],
		["previous session", { key: "PageUp", control: true }, PREVIOUS_SESSION_SHORTCUT_CHANNEL],
		["next session", { key: "PageDown", control: true }, NEXT_SESSION_SHORTCUT_CHANNEL],
		["previous tab", { key: "Tab", control: true, shift: true }, PREVIOUS_TAB_SHORTCUT_CHANNEL],
		["next tab", { key: "Tab", control: true }, NEXT_TAB_SHORTCUT_CHANNEL],
	] as const)("forwards the Windows/Linux %s shortcut", (_label, input, channel) => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target);

		const event = source.emit(input);

		expect(target.send).toHaveBeenCalledWith(channel);
		expect(event.preventDefault).toHaveBeenCalledTimes(1);
	});

	it.each([
		["macOS", true, "toggle-sidebar", { key: "b", meta: true }],
		["macOS", true, "toggle-terminal", { key: "`", code: "Backquote", control: true }],
		["macOS", true, "toggle-terminal", { key: "j", meta: true }],
		["macOS", true, "toggle-diff", { key: "D", meta: true, shift: true }],
		["macOS", true, "toggle-inspector", { key: "B", meta: true, shift: true }],
		["macOS", true, "toggle-inspector", { key: "b", code: "KeyB", meta: true, alt: true }],
		["macOS", true, "command-palette", { key: "k", meta: true }],
		["macOS", true, "command-palette", { key: "P", meta: true, shift: true }],
		["macOS", true, "quick-open-file", { key: "p", meta: true }],
		["macOS", true, "reopen-closed-tab", { key: "T", meta: true, shift: true }],
		["macOS", true, "history-back", { key: "[", code: "BracketLeft", meta: true }],
		["macOS", true, "history-forward", { key: "]", code: "BracketRight", meta: true }],
		["macOS", true, "archive-session", { key: "A", meta: true, shift: true }],
		["macOS", true, "open-in-editor", { key: "o", meta: true }],
		["macOS", true, "copy-session-path", { key: "C", meta: true, shift: true }],
		["macOS", true, "model-picker", { key: "M", meta: true, shift: true }],
		["macOS", true, "previous-attention-session", { key: "ArrowUp", meta: true, alt: true, shift: true }],
		["macOS", true, "next-attention-session", { key: "ArrowDown", meta: true, alt: true, shift: true }],
		["Windows/Linux", false, "history-back", { key: "ArrowLeft", alt: true }],
		["Windows/Linux", false, "toggle-terminal", { key: "`", code: "Backquote", control: true }],
	] as const)("forwards %s %s through the generic channel", (_platform, isMac, id, input) => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, isMac, target);

		const event = source.emit(input);

		expect(target.send).toHaveBeenCalledWith(APP_SHORTCUT_CHANNEL, { id });
		expect(event.preventDefault).toHaveBeenCalledTimes(1);
	});

	it.each([
		["previous session", { key: "{", code: "BracketLeft", meta: true, shift: true }, PREVIOUS_SESSION_SHORTCUT_CHANNEL],
		["next session", { key: "}", code: "BracketRight", meta: true, shift: true }, NEXT_SESSION_SHORTCUT_CHANNEL],
		["previous session alias", { key: "ArrowUp", meta: true, alt: true }, PREVIOUS_SESSION_SHORTCUT_CHANNEL],
		["next session alias", { key: "ArrowDown", meta: true, alt: true }, NEXT_SESSION_SHORTCUT_CHANNEL],
		["previous tab alias", { key: "ArrowLeft", meta: true, alt: true }, PREVIOUS_TAB_SHORTCUT_CHANNEL],
		["next tab alias", { key: "ArrowRight", meta: true, alt: true }, NEXT_TAB_SHORTCUT_CHANNEL],
	] as const)("forwards the macOS %s shortcut", (_label, input, channel) => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, true, target);

		source.emit(input);

		expect(target.send).toHaveBeenCalledWith(channel);
	});

	it.each([1, 5, 9])("sends the zero-based index for jump-to-session with digit %i", (digit) => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, true, target);

		source.emit({ key: String(digit), meta: true });

		expect(target.send).toHaveBeenCalledWith(APP_SHORTCUT_CHANNEL, { id: "jump-to-session", index: digit - 1 });
	});

	it.each(["k", "p"])("lets a focused terminal keep plain Ctrl+%s on Windows/Linux", (key) => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, false, target, false, () => ({}), () => false, () => true, undefined, () => true);

		const event = source.emit({ key, control: true });

		expect(target.send).not.toHaveBeenCalled();
		expect(event.preventDefault).not.toHaveBeenCalled();
	});

	it("still opens the palette from a focused terminal on macOS", () => {
		const source = fakeSource();
		const target = fakeTarget();
		attachAppShortcuts(source, true, target, false, () => ({}), () => false, () => true, undefined, () => true);

		source.emit({ key: "k", meta: true });

		expect(target.send).toHaveBeenCalledWith(APP_SHORTCUT_CHANNEL, { id: "command-palette" });
	});

	it("reads live user overrides without reattaching the listener", () => {
		const source = fakeSource();
		const target = fakeTarget();
		let overrides = {};
		attachAppShortcuts(source, false, target, false, () => overrides);

		source.emit({ key: "`", code: "Backquote", control: true });
		overrides = {
			"toggle-terminal": [
				{ key: "j", ctrl: true, meta: false, shift: false, alt: false },
			],
		};
		source.emit({ key: "`", code: "Backquote", control: true });
		source.emit({ key: "j", control: true });

		expect(target.send).toHaveBeenCalledTimes(2);
		expect(target.send).toHaveBeenLastCalledWith(APP_SHORTCUT_CHANNEL, { id: "toggle-terminal" });
	});

	it("does not intercept application shortcuts while a binding is being recorded", () => {
		const source = fakeSource();
		const target = fakeTarget();
		let recording = true;
		attachAppShortcuts(source, false, target, false, () => ({}), () => recording);

		const recordingEvent = source.emit({ key: "/", control: true });
		recording = false;
		const activeEvent = source.emit({ key: "/", control: true });

		expect(recordingEvent.preventDefault).not.toHaveBeenCalled();
		expect(activeEvent.preventDefault).toHaveBeenCalledTimes(1);
		expect(target.send).toHaveBeenCalledTimes(1);
		expect(target.send).toHaveBeenCalledWith(KEYBOARD_SHORTCUTS_HELP_CHANNEL);
	});

	it.each([
		["macOS", true, { key: "=", code: "Equal", meta: true }],
		["Windows/Linux", false, { key: "+", code: "Equal", control: true }],
	] as const)("routes primary-modifier plus/minus to the focused terminal on %s and preserves app zoom otherwise", (_platform, isMac, input) => {
		const source = fakeSource();
		const target = fakeTarget();
		let terminalFocused = true;
		attachAppShortcuts(
			source,
			isMac,
			target,
			false,
			() => ({}),
			() => false,
			() => true,
			undefined,
			() => terminalFocused,
		);

		const focusedEvent = source.emit(input);

		expect(focusedEvent.preventDefault).toHaveBeenCalledOnce();
		expect(target.send).toHaveBeenCalledWith(TERMINAL_FONT_SIZE_SHORTCUT_CHANNEL, 1);

		target.send.mockClear();
		terminalFocused = false;
		const appZoomEvent = source.emit(
			isMac
				? { key: "-", code: "Minus", meta: true }
				: { key: "-", code: "Minus", control: true },
		);

		expect(appZoomEvent.preventDefault).not.toHaveBeenCalled();
		expect(target.send).not.toHaveBeenCalled();
	});
});
