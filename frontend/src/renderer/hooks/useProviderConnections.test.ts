import { describe, expect, it, vi } from "vitest";
import type { CloudCpClient, CloudCpProviderConnection } from "../lib/cloud-cp";
import { CloudCpError } from "../lib/cloud-cp/errors";
import { hasValidAgentConnection, loadProviderConnections } from "./useProviderConnections";

const connection = (provider: string, validationState: string) =>
	({ id: `${provider}-${validationState}`, provider, validationState }) as unknown as CloudCpProviderConnection;

describe("hasValidAgentConnection", () => {
	it("accepts a validated coding-agent connection", () => {
		expect(hasValidAgentConnection([connection("claude-code", "valid")])).toBe(true);
	});

	it("ignores a validated GitHub token from the personal list", () => {
		expect(hasValidAgentConnection([connection("github", "valid")])).toBe(false);
	});

	it("ignores connections the control plane has not validated", () => {
		expect(hasValidAgentConnection([connection("codex", "invalid")])).toBe(false);
		expect(hasValidAgentConnection(undefined)).toBe(false);
	});
});

describe("loadProviderConnections", () => {
	it("uses org-scoped connections only when the personal endpoint is missing", async () => {
		const codex = connection("codex", "valid");
		const client = {
			listUserProviderConnections: vi.fn().mockRejectedValue(new CloudCpError("Not found", { status: 404 })),
			listOrgProviderConnections: vi.fn().mockResolvedValue({ providerConnections: [codex] }),
		} as unknown as CloudCpClient;
		expect(await loadProviderConnections(client, "org-1")).toEqual({ connections: [codex], chatSessionModels: [] });
		expect(client.listOrgProviderConnections).toHaveBeenCalledWith("org-1");
	});
	it("enables Terra chat only when the server advertises it", async () => {
		const codex = connection("codex", "valid");
		const client = {
			listUserProviderConnections: vi.fn().mockResolvedValue({
				providerConnections: [codex], chatSessionModels: ["gpt-5.6-terra"],
			}),
			listOrgProviderConnections: vi.fn(),
		} as unknown as CloudCpClient;
		expect(await loadProviderConnections(client, "org-1")).toEqual({
			connections: [codex], chatSessionModels: ["gpt-5.6-terra"],
		});
		expect(client.listOrgProviderConnections).not.toHaveBeenCalled();
	});
	it("keeps an older personal-provider server in terminal mode", async () => {
		const client = {
			listUserProviderConnections: vi.fn().mockResolvedValue({ providerConnections: [] }),
		} as unknown as CloudCpClient;
		expect((await loadProviderConnections(client, "org-1")).chatSessionModels).toEqual([]);
	});
	it("does not hide unrelated connection errors", async () => {
		const client = {
			listUserProviderConnections: vi.fn().mockRejectedValue(new CloudCpError("Forbidden", { status: 403 })),
			listOrgProviderConnections: vi.fn(),
		} as unknown as CloudCpClient;
		await expect(loadProviderConnections(client, "org-1")).rejects.toMatchObject({ status: 403 });
		expect(client.listOrgProviderConnections).not.toHaveBeenCalled();
	});
});
