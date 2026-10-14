import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const hooks = vi.hoisted(() => ({
	session: null as unknown,
	status: "authenticated" as string,
	setCloudUser: vi.fn(async (_id: string | null) => hooks.retired),
	identifyCloudUser: vi.fn(async () => undefined),
	clearCloudUser: vi.fn(),
	retireBootstrapId: vi.fn(),
	retired: false,
}));
vi.mock("../lib/cloud-session", () => ({ useCloudSession: () => ({ session: hooks.session, status: hooks.status }) }));
vi.mock("../lib/bridge", () => ({ aoBridge: { telemetry: { setCloudUser: hooks.setCloudUser } } }));
vi.mock("../lib/telemetry", () => ({ identifyCloudUser: hooks.identifyCloudUser, clearCloudUser: hooks.clearCloudUser, retireBootstrapId: hooks.retireBootstrapId }));

import { TelemetryIdentityRuntime } from "./TelemetryIdentityRuntime";

const account = (authProvider: "workos" | "local") => ({
	authProvider,
	user: { id: "user_01H", email: "dev@example.com", displayName: "Dev" },
	storedAt: "2026-10-08T00:00:00Z",
});

describe("TelemetryIdentityRuntime", () => {
	beforeEach(() => {
		vi.clearAllMocks();
		hooks.status = "authenticated";
		hooks.retired = false;
	});

	it("drops the launch's install ID when main reports it retired on sign-out", async () => {
		hooks.session = null;
		hooks.retired = true;
		render(<TelemetryIdentityRuntime />);
		await vi.waitFor(() => expect(hooks.retireBootstrapId).toHaveBeenCalledTimes(1));
	});

	it("keeps the install ID when main does not report it retired", async () => {
		hooks.session = null;
		render(<TelemetryIdentityRuntime />);
		await Promise.resolve();
		expect(hooks.retireBootstrapId).not.toHaveBeenCalled();
	});

	it("says nothing while the session is still loading, so a launch is not read as a sign-out", () => {
		hooks.session = null;
		hooks.status = "loading";
		render(<TelemetryIdentityRuntime />);
		expect(hooks.setCloudUser).not.toHaveBeenCalled();
		expect(hooks.clearCloudUser).not.toHaveBeenCalled();
	});

	it("identifies a WorkOS sign-in and hands the user id (not the email) to main", () => {
		hooks.session = account("workos");
		render(<TelemetryIdentityRuntime />);
		expect(hooks.identifyCloudUser).toHaveBeenCalledWith({ id: "user_01H", email: "dev@example.com" });
		expect(hooks.setCloudUser).toHaveBeenCalledWith("user_01H");
		expect(JSON.stringify(hooks.setCloudUser.mock.calls)).not.toContain("dev@example.com");
	});

	it("never identifies the dev-only local provider", () => {
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
