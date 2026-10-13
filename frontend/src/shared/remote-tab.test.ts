import { describe, expect, it } from "vitest";
import { REMOTE_TAB_REPORT, parseRemoteTabReport, pinnedRemoteTab } from "./remote-tab";

describe("parseRemoteTabReport", () => {
	it("reads a viewer's report of the agent browser page", () => {
		const report = parseRemoteTabReport(REMOTE_TAB_REPORT + JSON.stringify({
			tabId: "R1", url: "https://a.example/x", title: "A", favicon: "https://a.example/icon.png", loading: true, back: true, fwd: false,
			follow: "R2", closed: { next: "R0" }, driving: true, tabs: [{ id: "R1", title: "A", url: "https://a.example/x" }, { title: "no id" }],
		}));
		expect(report).toEqual({
			tabId: "R1", url: "https://a.example/x", title: "A", favicon: "https://a.example/icon.png", loading: true, back: true, fwd: false,
			follow: "R2", closed: { next: "R0" }, shown: false, tabs: ["R1"],
		});
	});

	it("rejects anything else, and lets only web addresses reach the address bar", () => {
		expect(parseRemoteTabReport("hello")).toBeNull();
		expect(parseRemoteTabReport(REMOTE_TAB_REPORT + "{not json")).toBeNull();
		expect(parseRemoteTabReport(REMOTE_TAB_REPORT + JSON.stringify({ url: "https://a.example/" }))).toBeNull();
		const report = parseRemoteTabReport(REMOTE_TAB_REPORT + JSON.stringify({ tabId: "R1", url: "javascript:alert(1)", favicon: "data:image/png;base64,x" }));
		expect(report).toMatchObject({ url: "", favicon: "", tabs: [] });
	});

	it("finds the agent browser tab a viewer URL is pinned to", () => {
		expect(pinnedRemoteTab("https://ao-preview-x.localhost:1/v/cdp?embed=1&tab=R7")).toBe("R7");
		expect(pinnedRemoteTab("https://ao-preview-x.localhost:1/v/cdp?embed=1")).toBe("");
		expect(pinnedRemoteTab("https://example.com/?tab=R7")).toBe("");
	});
});
