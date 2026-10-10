import { useEffect, useRef } from "react";
import type { AppShortcutEvent, AppShortcutId } from "../../shared/shortcuts";
import { aoBridge } from "../lib/bridge";
import { useUiStore } from "../stores/ui-store";

/** Focuses the first element matching `selector` once the next render has painted. */
export function focusAfterPaint(selector: string): void {
	window.requestAnimationFrame(() => {
		window.requestAnimationFrame(() => document.querySelector<HTMLElement>(selector)?.focus());
	});
}

/**
 * Runs `handler` when the main process reports that shortcut `id` was pressed.
 * Main matches the chord before xterm or a native browser view can swallow it,
 * so this works wherever focus is. Pass `enabled: false` to stop listening
 * (e.g. while the owning surface is not the active one).
 */
export function useAppShortcut(
	id: AppShortcutId,
	handler: (event: AppShortcutEvent) => void,
	enabled = true,
): void {
	const handlerRef = useRef(handler);
	handlerRef.current = handler;
	useEffect(() => {
		if (!enabled) return;
		return aoBridge.app.onShortcut((event) => {
			if (event.id === id) handlerRef.current(event);
		});
	}, [id, enabled]);
}

/** True while the global Settings page covers the routed (session) page. */
export function isSettingsPageOpen(): boolean {
	return useUiStore.getState().settingsModal?.scope === "global";
}

/**
 * For commands that act on the session on screen (archive, open in editor,
 * copy path, panel toggles). The session page stays mounted but hidden under
 * Settings, so these stand down there instead of acting on an invisible session.
 */
export function useSessionShortcut(
	id: AppShortcutId,
	handler: (event: AppShortcutEvent) => void,
	enabled = true,
): void {
	useAppShortcut(
		id,
		(event) => {
			if (!isSettingsPageOpen()) handler(event);
		},
		enabled,
	);
}
