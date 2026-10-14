import type { BaseWindow } from "electron";

type VibrancyWindow = Pick<BaseWindow, "isDestroyed" | "setVibrancy">;

export type WindowTranslucency = {
	/** Whether the user's window transparency setting is on (macOS only). */
	readonly enabled: boolean;
	/** Record the renderer's setting and apply it to the current window. */
	set: (enabled: boolean) => void;
	/**
	 * Apply the remembered setting and repaint the window background. `rebuild` drops and
	 * recreates the native vibrancy view, for events after which macOS may have
	 * left the old one stale (wake, unlock, display or GPU changes).
	 */
	reapply: (options?: { rebuild?: boolean }) => void;
};

/**
 * Main-process owner of macOS window transparency.
 *
 * The setting survives window recreation, so a reopened window gets vibrancy at
 * creation instead of waiting on its renderer. Every call applies to the real
 * window: setVibrancy with the same material is cheap, and a cached "already
 * applied" flag drifts from the window's actual state.
 */
export function createWindowTranslucency(options: {
	platform: NodeJS.Platform;
	getWindow: () => VibrancyWindow | null;
	/** Repaints the native window background (clear while translucent). */
	syncBackground: () => void;
}): WindowTranslucency {
	let enabled = false;

	const apply = (rebuild: boolean, clear: boolean): void => {
		const window = options.getWindow();
		if (!window || window.isDestroyed()) return;
		if (enabled) {
			if (rebuild) window.setVibrancy(null);
			window.setVibrancy("under-window");
		} else if (clear) {
			window.setVibrancy(null);
		}
		options.syncBackground();
	};

	return {
		get enabled() {
			return enabled;
		},
		set(next) {
			if (options.platform !== "darwin") return;
			enabled = next;
			apply(false, true);
		},
		// Off macOS `enabled` never turns on, so this only repaints the background.
		reapply(reapplyOptions) {
			apply(reapplyOptions?.rebuild === true, false);
		},
	};
}
