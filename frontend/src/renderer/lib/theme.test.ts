import { afterEach, describe, expect, it, vi } from "vitest";
import {
	applyDocumentTheme,
	applyDocumentThemeStyle,
	previewDocumentThemeStyle,
	runThemeTransition,
} from "./theme";

describe("runThemeTransition", () => {
	afterEach(() => {
		Reflect.deleteProperty(document, "startViewTransition");
		delete document.documentElement.dataset.theme;
		delete document.documentElement.dataset.styleTheme;
		delete document.documentElement.dataset.themeTransition;
		document.documentElement.style.colorScheme = "";
	});

	it("runs the update immediately when View Transitions are unavailable", () => {
		Reflect.deleteProperty(document, "startViewTransition");
		const update = vi.fn(() => {
			applyDocumentTheme("light");
		});

		runThemeTransition(update);

		expect(update).toHaveBeenCalledOnce();
		expect(document.documentElement.dataset.theme).toBe("light");
		expect(document.documentElement.style.colorScheme).toBe("light");
	});

	it("wraps the update in startViewTransition when available", () => {
		const startViewTransition = vi.fn((callback: () => void) => {
			callback();
			return {
				finished: Promise.resolve(),
				ready: Promise.resolve(),
				updateCallbackDone: Promise.resolve(),
				skipTransition: () => undefined,
			};
		});
		Object.defineProperty(document, "startViewTransition", {
			configurable: true,
			writable: true,
			value: startViewTransition,
		});
		const update = vi.fn(() => {
			applyDocumentThemeStyle("github");
		});

		runThemeTransition(update);

		expect(startViewTransition).toHaveBeenCalledOnce();
		expect(update).toHaveBeenCalledOnce();
		expect(document.documentElement.dataset.styleTheme).toBe("github");
		expect(document.documentElement.dataset.themeTransition).toBe("active");
	});

	it("suppresses per-element transitions while the theme tokens swap", async () => {
		Reflect.deleteProperty(document, "startViewTransition");
		const update = vi.fn(() => {
			applyDocumentTheme("dark");
		});

		runThemeTransition(update);

		expect(document.documentElement.dataset.themeTransition).toBe("active");
		await new Promise((resolve) => requestAnimationFrame(resolve));
		expect(document.documentElement.dataset.themeTransition).toBeUndefined();
	});
});

describe("applyDocumentThemeStyle", () => {
	afterEach(() => {
		delete document.documentElement.dataset.styleTheme;
	});

	it("clears data-style-theme for orchestrate", () => {
		document.documentElement.dataset.styleTheme = "nord";
		applyDocumentThemeStyle("orchestrate");
		expect(document.documentElement.dataset.styleTheme).toBeUndefined();
	});
});

describe("previewDocumentThemeStyle", () => {
	const nextFrame = () => new Promise((resolve) => requestAnimationFrame(resolve));

	afterEach(() => {
		Reflect.deleteProperty(document, "startViewTransition");
		delete document.documentElement.dataset.styleTheme;
		delete document.documentElement.dataset.themeTransition;
	});

	it("swaps the theme synchronously without a View Transition and restores the saved one", async () => {
		const startViewTransition = vi.fn();
		Object.defineProperty(document, "startViewTransition", {
			configurable: true,
			writable: true,
			value: startViewTransition,
		});

		previewDocumentThemeStyle("nord", "github");
		expect(document.documentElement.dataset.styleTheme).toBe("nord");
		expect(document.documentElement.dataset.themeTransition).toBe("active");

		previewDocumentThemeStyle("dracula", "github");
		expect(document.documentElement.dataset.styleTheme).toBe("dracula");

		previewDocumentThemeStyle(null, "github");
		expect(document.documentElement.dataset.styleTheme).toBe("github");
		expect(document.documentElement.dataset.themeTransition).toBe("active");
		await nextFrame();
		await nextFrame();
		expect(document.documentElement.dataset.themeTransition).toBeUndefined();
		expect(startViewTransition).not.toHaveBeenCalled();
	});

	it("leaves the document alone when no preview is active", () => {
		document.documentElement.dataset.styleTheme = "nord";
		previewDocumentThemeStyle(null, "github");
		expect(document.documentElement.dataset.styleTheme).toBe("nord");
	});
});
