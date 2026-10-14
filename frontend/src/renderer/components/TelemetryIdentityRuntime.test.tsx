import { act, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hooks = vi.hoisted(() => ({
	session: null as unknown,
	status: "authenticated" as string,
	setCloudUser: vi.fn(async (_id: string | null) => true),
	identifyCloudUser: vi.fn(async () => undefined),
	clearCloudUser: vi.fn(),
}));
vi.mock("../lib/cloud-session", () => ({ useCloudSession: () => ({ session: hooks.session, status: hooks.status }) }));
vi.mock("../lib/bridge", () => ({ aoBridge: { telemetry: { setCloudUser: hooks.setCloudUser } } }));
vi.mock("../lib/telemetry", () => ({ identifyCloudUser: hooks.identifyCloudUser, clearCloudUser: hooks.clearCloudUser }));

import { BINDING_RETRY_MS, TelemetryIdentityRuntime } from "./TelemetryIdentityRuntime";

const account = (authProvider: "workos" | "local") => ({
	authProvider,
	user: { id: "user_01H", email: "dev@example.com", displayName: "Dev" },
	storedAt: "2026-10-08T00:00:00Z",
});

describe("TelemetryIdentityRuntime", () => {
	beforeEach(() => {
		vi.clearAllMocks();
		hooks.status = "authenticated";
		hooks.setCloudUser.mockImplementation(async () => true);
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	it("says nothing while the session is still loading, so a launch is not read as a sign-out", () => {
		hooks.session = null;
		hooks.status = "loading";
		render(<TelemetryIdentityRuntime />);
		expect(hooks.setCloudUser).not.toHaveBeenCalled();
		expect(hooks.clearCloudUser).not.toHaveBeenCalled();
	});

	it("hands the user id (not the email) to main and identifies once the daemon acknowledged it", async () => {
		hooks.session = account("workos");
		render(<TelemetryIdentityRuntime />);
		await vi.waitFor(() => expect(hooks.identifyCloudUser).toHaveBeenCalledWith({ id: "user_01H", email: "dev@example.com" }));
		expect(hooks.setCloudUser).toHaveBeenCalledWith("user_01H");
		expect(JSON.stringify(hooks.setCloudUser.mock.calls)).not.toContain("dev@example.com");
	});

	it("never identifies without an acknowledgement, and retries until the daemon records the user", async () => {
		vi.useFakeTimers();
		hooks.session = account("workos");
		// Daemon unavailable, then a 500, then recovered.
		hooks.setCloudUser.mockResolvedValueOnce(false).mockResolvedValueOnce(false).mockResolvedValue(true);
		render(<TelemetryIdentityRuntime />);
		await act(async () => {
			await vi.advanceTimersByTimeAsync(0);
		});
		expect(hooks.identifyCloudUser).not.toHaveBeenCalled();
		await act(async () => {
			await vi.advanceTimersByTimeAsync(BINDING_RETRY_MS[0]);
		});
		expect(hooks.identifyCloudUser).not.toHaveBeenCalled();
		await act(async () => {
			await vi.advanceTimersByTimeAsync(BINDING_RETRY_MS[1]);
		});
		expect(hooks.setCloudUser).toHaveBeenCalledTimes(3);
		expect(hooks.identifyCloudUser).toHaveBeenCalledTimes(1);
	});

	it("a sign-out while the binding is still pending cancels the identify for good", async () => {
		vi.useFakeTimers();
		hooks.session = account("workos");
		hooks.setCloudUser.mockImplementation(async (id) => id === null);
		const view = render(<TelemetryIdentityRuntime />);
		await act(async () => {
			await vi.advanceTimersByTimeAsync(0);
		});
		hooks.session = null;
		view.rerender(<TelemetryIdentityRuntime />);
		hooks.setCloudUser.mockImplementation(async () => true); // the daemon recovers afterwards
		await act(async () => {
			await vi.advanceTimersByTimeAsync(BINDING_RETRY_MS.reduce((a, b) => a + b, 0));
		});
		expect(hooks.identifyCloudUser).not.toHaveBeenCalled();
		expect(hooks.clearCloudUser).toHaveBeenCalled();
		expect(hooks.setCloudUser).toHaveBeenLastCalledWith(null);
	});

	it("never identifies the dev-only local provider", async () => {
		hooks.session = account("local");
		render(<TelemetryIdentityRuntime />);
		expect(hooks.identifyCloudUser).not.toHaveBeenCalled();
		expect(hooks.setCloudUser).toHaveBeenCalledWith(null);
	});

	it("clears the identity when signed out", () => {
		hooks.session = null;
		render(<TelemetryIdentityRuntime />);
		expect(hooks.clearCloudUser).toHaveBeenCalled();
		expect(hooks.setCloudUser).toHaveBeenCalledWith(null);
	});
});
