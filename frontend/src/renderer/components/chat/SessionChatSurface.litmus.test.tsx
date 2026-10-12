import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ConversationSnapshot } from "../../types/conversation";
import type { WorkspaceSession } from "../../types/workspace";

const { getMock, postMock, sendMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
	sendMock: vi.fn(),
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	getApiBaseUrl: () => "",
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		const message = (error as { message?: unknown } | undefined)?.message;
		return typeof message === "string" ? message : fallback;
	},
}));

vi.mock("../../hooks/useConversation", () => ({
	clearConversationProviderCatalogs: vi.fn(),
	conversationQueryKey: (sessionId: string) => ["conversation", sessionId],
	invalidateConversationProviderCatalogs: vi.fn(),
	useConversation: (sessionId: string) => ({
		snapshot: {
			activeBranchId: "branch-root",
			branchPoints: [],
			capabilities: [],
			conversationId: `conv-${sessionId}`,
			sessionId,
			harness: "codex",
			mode: "chat",
			controller: { state: "ready" },
			items: [],
			turns: [],
			settings: {},
			mcpServers: [],
			oldestSequence: 0,
			latestSequence: 0,
			hasMoreBefore: false,
		} satisfies ConversationSnapshot,
		isLoading: false,
		hasOlder: false,
		isLoadingOlder: false,
		loadOlder: vi.fn(),
	}),
	useConversationCommands: () => ({ busy: false, send: sendMock, acknowledgeAcceptedTurn: vi.fn() }),
	useConversationConfigOptions: vi.fn(() => ({ options: [], loaded: false, error: undefined })),
	useConversationModels: vi.fn(() => ({ models: [] })),
	useConversationSkills: vi.fn(() => ({ skills: [] })),
	useStageAttachments: () => undefined,
	useWorkspaceFilePaths: () => ({ paths: [], truncated: false }),
}));

vi.mock("../../hooks/useAgentSwitchVisibility", () => ({
	useAgentSwitchPresentationVisibility: vi.fn(),
	useAgentSwitchRouteVisibility: vi.fn(),
}));

// The composer is its own suite; this stub submits whatever the test types.
vi.mock("./ChatWorkspace", () => ({
	ChatWorkspace: ({
		onSend,
	}: {
		onSend?: (text: string, attachments?: { mimeType: string; data: string }[]) => void | Promise<unknown>;
	}) => {
		const submit = (text: string, attachments?: { mimeType: string; data: string }[]) => () => {
			void Promise.resolve(onSend?.(text, attachments)).catch(() => {});
		};
		return (
			<div>
				<button type="button" onClick={submit("test PR #6473")}>send test PR number</button>
				<button type="button" onClick={submit("litmus https://github.com/acme/app/pull/77")}>send litmus url</button>
				<button type="button" onClick={submit("litmus 5")}>send litmus bare</button>
				<button type="button" onClick={submit("test prs please")}>send ordinary</button>
				<button type="button" onClick={submit("test PR #9", [{ mimeType: "image/png", data: "AA" }])}>send with image</button>
			</div>
		);
	},
}));

import { SessionChatSurface } from "./SessionChatSurface";

const worker = {
	id: "ao-3",
	workspaceId: "proj-1",
	workspaceName: "my-app",
	title: "worker",
	provider: "codex",
	kind: "worker",
	mode: "chat",
	status: "working",
	updatedAt: "2026-08-08T00:00:00Z",
	prs: [],
} satisfies WorkspaceSession;
const orchestrator = { ...worker, id: "ao-1-orchestrator", kind: "orchestrator" } satisfies WorkspaceSession;

function renderSurface(session: WorkspaceSession, onOpenLitmus = vi.fn(), hostId?: string) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	const wrap = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
	render(<SessionChatSurface session={session} hostId={hostId} onOpenLitmus={onOpenLitmus} />, { wrapper: wrap });
	return onOpenLitmus;
}

beforeEach(() => {
	getMock.mockReset().mockResolvedValue({ data: { runs: [{ repo: "acme/app" }] }, error: undefined });
	postMock.mockReset().mockResolvedValue({ data: { name: "litmus-6473", sessionId: "ao-9" }, error: undefined });
	sendMock.mockReset().mockResolvedValue(undefined);
});

describe("Litmus chat command intercept", () => {
	it("starts a run from the orchestrator chat, shows the status line, and does not message the agent", async () => {
		const onOpenLitmus = renderSurface(orchestrator);
		await userEvent.click(screen.getByText("send litmus url"));

		await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/testing/start", {
			body: { projectId: "proj-1", prUrl: "https://github.com/acme/app/pull/77" },
		}));
		const notice = await screen.findByText("Litmus started on #77 (session ao-9)");
		expect(notice).toBeInTheDocument();
		expect(sendMock).not.toHaveBeenCalled();

		await userEvent.click(screen.getByRole("link", { name: "Open Litmus" }));
		expect(onOpenLitmus).toHaveBeenCalledWith("proj-1");
	});

	it("resolves a bare PR number against the repo of the project's latest run", async () => {
		renderSurface(orchestrator);
		await userEvent.click(screen.getByText("send test PR number"));

		await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/testing/start", {
			body: { projectId: "proj-1", prUrl: "https://github.com/acme/app/pull/6473" },
		}));
		expect(await screen.findByText("Litmus started on #6473 (session ao-9)")).toBeInTheDocument();
		expect(sendMock).not.toHaveBeenCalled();
	});

	it("explains a bare number it cannot resolve instead of sending it to the agent", async () => {
		getMock.mockResolvedValue({ data: { runs: [] }, error: undefined });
		renderSurface(orchestrator);
		await userEvent.click(screen.getByText("send litmus bare"));

		expect(await screen.findByRole("alert")).toHaveTextContent("Enter the full PR URL");
		expect(postMock).not.toHaveBeenCalled();
		expect(sendMock).not.toHaveBeenCalled();
	});

	it("shows the daemon's error when the start call fails", async () => {
		postMock.mockResolvedValue({ data: undefined, error: { message: "no such PR" } });
		renderSurface(orchestrator);
		await userEvent.click(screen.getByText("send litmus url"));

		expect(await screen.findByRole("alert")).toHaveTextContent("no such PR");
		expect(sendMock).not.toHaveBeenCalled();
	});

	it("sends ordinary messages to the agent untouched", async () => {
		renderSurface(orchestrator);
		await userEvent.click(screen.getByText("send ordinary"));

		await waitFor(() => expect(sendMock).toHaveBeenCalledWith(expect.objectContaining({ text: "test prs please" })));
		expect(postMock).not.toHaveBeenCalled();
	});

	it("leaves a message with attachments for the agent", async () => {
		renderSurface(orchestrator);
		await userEvent.click(screen.getByText("send with image"));

		await waitFor(() => expect(sendMock).toHaveBeenCalledWith(expect.objectContaining({ text: "test PR #9" })));
		expect(postMock).not.toHaveBeenCalled();
	});

	it("does not intercept in a worker session chat", async () => {
		renderSurface(worker);
		await userEvent.click(screen.getByText("send test PR number"));

		await waitFor(() => expect(sendMock).toHaveBeenCalledWith(expect.objectContaining({ text: "test PR #6473" })));
		expect(postMock).not.toHaveBeenCalled();
		expect(screen.queryByTestId("litmus-chat-notice")).not.toBeInTheDocument();
	});

	it("does not intercept in a remote host's orchestrator chat", async () => {
		renderSurface(orchestrator, vi.fn(), "host-2");
		await userEvent.click(screen.getByText("send test PR number"));

		await waitFor(() => expect(sendMock).toHaveBeenCalled());
		expect(postMock).not.toHaveBeenCalled();
	});
});
