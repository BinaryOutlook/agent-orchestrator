import { describe, expect, it } from "vitest";
import { formatStarted, resolvePrInput } from "./litmus-format";

describe("resolvePrInput", () => {
	it("reads a bare number or #N against the project's repo", () => {
		expect(resolvePrInput("34", "acme/app")).toBe("https://github.com/acme/app/pull/34");
		expect(resolvePrInput(" #34 ", "acme/app")).toBe("https://github.com/acme/app/pull/34");
	});

	it("cannot resolve a bare number when the project has no repo", () => {
		expect(resolvePrInput("34", undefined)).toBeNull();
	});

	it("passes a URL or owner/repo#N through and rejects empty input", () => {
		expect(resolvePrInput("https://github.com/a/b/pull/1", undefined)).toBe("https://github.com/a/b/pull/1");
		expect(resolvePrInput("a/b#1", "acme/app")).toBe("a/b#1");
		expect(resolvePrInput("  ", "acme/app")).toBeNull();
	});
});

describe("formatStarted", () => {
	it("shows a plain hyphen for a date it cannot read", () => {
		expect(formatStarted("not a date")).toBe("-");
	});
});
