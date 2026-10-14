import { describe, expect, it, vi } from "vitest";
import { createWindowTranslucency } from "./window-translucency";

function setup(platform: NodeJS.Platform = "darwin") {
	const window = { isDestroyed: vi.fn(() => false), setVibrancy: vi.fn() };
	let current: typeof window | null = window;
	const syncBackground = vi.fn();
	const translucency = createWindowTranslucency({ platform, getWindow: () => current, syncBackground });
	return {
		translucency,
		window,
		syncBackground,
		replaceWindow: (next: typeof window | null) => {
			current = next;
		},
	};
}

describe("createWindowTranslucency", () => {
	it("applies vibrancy every time the renderer sends the setting", () => {
		const { translucency, window, syncBackground } = setup();

		translucency.set(true);
		translucency.set(true);

		expect(window.setVibrancy).toHaveBeenCalledTimes(2);
		expect(window.setVibrancy).toHaveBeenLastCalledWith("under-window");
		expect(syncBackground).toHaveBeenCalledTimes(2);
		expect(translucency.enabled).toBe(true);
	});

	it("removes vibrancy when turned off", () => {
		const { translucency, window } = setup();

		translucency.set(true);
		translucency.set(false);

		expect(window.setVibrancy).toHaveBeenLastCalledWith(null);
		expect(translucency.enabled).toBe(false);
	});

	it("applies a remembered setting to a replacement window", () => {
		const { translucency, window, replaceWindow } = setup();
		translucency.set(true);
		const next = { isDestroyed: vi.fn(() => false), setVibrancy: vi.fn() };

		replaceWindow(null);
		translucency.reapply();
		replaceWindow(next);
		translucency.reapply();

		expect(window.setVibrancy).toHaveBeenCalledTimes(1);
		expect(next.setVibrancy).toHaveBeenCalledWith("under-window");
	});

	it("remembers a setting sent while no window exists", () => {
		const { translucency, window, syncBackground, replaceWindow } = setup();
		replaceWindow(null);

		translucency.set(true);
		expect(syncBackground).not.toHaveBeenCalled();

		replaceWindow(window);
		translucency.reapply();
		expect(window.setVibrancy).toHaveBeenCalledWith("under-window");
	});

	it("rebuilds the vibrancy view on request", () => {
		const { translucency, window } = setup();
		translucency.set(true);
		window.setVibrancy.mockClear();

		translucency.reapply({ rebuild: true });

		expect(window.setVibrancy.mock.calls).toEqual([[null], ["under-window"]]);
	});

	it("only repaints the background when off", () => {
		const { translucency, window, syncBackground } = setup();

		translucency.reapply({ rebuild: true });

		expect(window.setVibrancy).not.toHaveBeenCalled();
		expect(syncBackground).toHaveBeenCalledTimes(1);
	});

	it("skips a destroyed window", () => {
		const { translucency, window, syncBackground } = setup();
		window.isDestroyed.mockReturnValue(true);

		translucency.set(true);

		expect(window.setVibrancy).not.toHaveBeenCalled();
		expect(syncBackground).not.toHaveBeenCalled();
	});

	it("never turns on outside macOS", () => {
		const { translucency, window, syncBackground } = setup("win32");

		translucency.set(true);
		translucency.reapply({ rebuild: true });

		expect(translucency.enabled).toBe(false);
		expect(window.setVibrancy).not.toHaveBeenCalled();
		expect(syncBackground).toHaveBeenCalledTimes(1);
	});
});
