import { describe, expect, it } from "vitest";
import { litmusLabel } from "./litmus-label";

describe("litmusLabel", () => {
	it("names the PR build and the main build after the PR", () => {
		expect(litmusLabel({ LITMUS_LEG: "head", LITMUS_PR: "6572" })?.title).toBe("Litmus: PR build #6572");
		expect(litmusLabel({ LITMUS_LEG: "base", LITMUS_PR: "6572" })?.title).toBe("Litmus: main for #6572");
	});
	it("still labels a window without a PR number", () => {
		expect(litmusLabel({ LITMUS_LEG: "head" })?.title).toBe("Litmus: PR build");
		expect(litmusLabel({ LITMUS_LEG: "base", LITMUS_PR: "x" })?.title).toBe("Litmus: main");
	});
	it("leaves a window Litmus did not launch alone", () => {
		expect(litmusLabel({})).toBeUndefined();
		expect(litmusLabel({ LITMUS_LEG: "other", LITMUS_PR: "1" })).toBeUndefined();
	});
});
