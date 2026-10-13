import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../types/workspace";

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	apiErrorMessage: (error: unknown) => (error as { message?: string })?.message ?? "request failed",
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

import { useLitmusChatCommand } from "./useLitmusChatCommand";

function wrapper(client: QueryClient) {
	return ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

const newClient = () => new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });

const session = (overrides: Partial<WorkspaceSession>) =>
	({ id: "app-1", workspaceId: "p1", kind: "worker", ...overrides }) as WorkspaceSession;

beforeEach(() => {
	getMock.mockReset();
	postMock.mockReset();
	getMock.mockResolvedValue({ data: { repo: "acme/app" }, error: undefined });
	postMock.mockResolvedValue({ data: { sessionId: "litmus-1" }, error: undefined });
});

async function run(target: WorkspaceSession, text: string, hostId?: string, hasExtras = false) {
	const { result } = renderHook(() => useLitmusChatCommand(target, hostId), { wrapper: wrapper(newClient()) });
	let handled = false;
	await act(async () => {
		handled = await result.current.intercept(text, hasExtras);
	});
	return { handled, result };
}

describe("useLitmusChatCommand", () => {
	it("intercepts in an ordinary project session, not just the orchestrator", async () => {
		const { handled, result } = await run(session({}), "test pr 6588");
		expect(handled).toBe(true);
		expect(postMock).toHaveBeenCalledWith("/api/v1/testing/start", {
			body: { projectId: "p1", prUrl: "https://github.com/acme/app/pull/6588" },
		});
		expect(result.current.notice).toEqual({ kind: "started", number: 6588, sessionId: "litmus-1" });
	});

	it("still intercepts in the orchestrator session", async () => {
		const { handled } = await run(session({ id: "app-orchestrator", kind: "orchestrator" }), "test PR #6588");
		expect(handled).toBe(true);
	});

	it("does not intercept a remote-host session", async () => {
		expect((await run(session({ hostId: "host-2" }), "test pr 6588")).handled).toBe(false);
		expect((await run(session({}), "test pr 6588", "host-2")).handled).toBe(false);
		expect(postMock).not.toHaveBeenCalled();
	});

	it("does not intercept a session without a project", async () => {
		expect((await run(session({ workspaceId: "" }), "test pr 6588")).handled).toBe(false);
		expect(postMock).not.toHaveBeenCalled();
	});

	it("does not intercept with attachments or a non-command message", async () => {
		expect((await run(session({}), "test pr 6588", undefined, true)).handled).toBe(false);
		expect((await run(session({}), "fix the failing test in PR 6588")).handled).toBe(false);
		expect(postMock).not.toHaveBeenCalled();
	});
});
