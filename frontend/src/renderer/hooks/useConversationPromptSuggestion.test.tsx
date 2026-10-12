import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { chatFixtureEmpty } from "../lib/chat-fixture";
import type { ConversationMessage, ConversationSnapshot } from "../types/conversation";

const { clientForSessionHost, post } = vi.hoisted(() => ({ clientForSessionHost: vi.fn(), post: vi.fn() }));
vi.mock("../lib/host-clients", () => ({ clientForSessionHost }));
import { useConversationPromptSuggestion } from "./useConversationPromptSuggestion";

const settled: ConversationSnapshot = {
	...chatFixtureEmpty,
	conversationId: "conversation-1",
	sessionId: "session-1",
	activeBranchId: "branch-1",
	controller: { state: "ready" },
	turns: [{ id: "turn-1", state: "completed", requestedAt: "2026-10-12T00:00:00Z" }],
};

function wrapperFor(client = new QueryClient()) {
	return ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
	clientForSessionHost.mockReset().mockReturnValue({ POST: post });
	post.mockReset().mockResolvedValue({ data: { turnId: "turn-1", prompt: "  Explain the fix.  " } });
});

describe("useConversationPromptSuggestion", () => {
	it("withdraws a cached suggestion when its source answer changes, but reuses unchanged replay", async () => {
		const message: ConversationMessage = {
			kind: "message", id: "reply-1", role: "assistant", origin: "provider", turnId: "turn-1",
			sequence: 1, revision: 1, text: "Fixed the race.", streaming: false, createdAt: "",
		};
		const initial: ConversationSnapshot = { ...settled, items: [message] };
		const { result, rerender } = renderHook(({ snapshot }: { snapshot: ConversationSnapshot }) => useConversationPromptSuggestion(snapshot, true), {
			wrapper: wrapperFor(), initialProps: { snapshot: initial },
		});
		await waitFor(() => expect(result.current?.text).toBe("Explain the fix."));
		const firstId = result.current?.id;
		rerender({ snapshot: { ...initial, items: [{ ...message, revision: 2 }, {
			kind: "activity", id: "usage-1", sequence: 2, revision: 1, activityKind: "usage",
			status: "completed", summary: "Updated usage", createdAt: "",
		}] } });
		expect(result.current?.id).toBe(firstId);
		expect(post).toHaveBeenCalledOnce();
		post.mockResolvedValue({ data: { turnId: "turn-1", prompt: "Fix the remaining race." } });
		rerender({ snapshot: { ...initial, items: [{ ...message, revision: 3, text: "The race is still present." }] } });
		expect(result.current).toBeUndefined();
		await waitFor(() => expect(result.current?.text).toBe("Fix the remaining race."));
		expect(result.current?.id).not.toBe(firstId);
		expect(post).toHaveBeenCalledTimes(2);
	});

	it("keys only the eight recent messages and the same Unicode context boundaries as the daemon", async () => {
		function message(id: string, role: "user" | "assistant", text: string): ConversationMessage {
			return { kind: "message", id, role, text, origin: "provider", sequence: 1, revision: 1, streaming: false, createdAt: "" };
		}
		const items = [message("old", "user", "Old context"), ...Array.from({ length: 6 }, (_, i) => message(`recent-${i}`, "user", "Recent context")),
			message("user", "user", `${"😀".repeat(2048)}old user ending`),
			message("assistant", "assistant", `old assistant beginning${"😀".repeat(2048)}`)];
		const { result, rerender } = renderHook(({ snapshot }) => useConversationPromptSuggestion(snapshot, true), {
			wrapper: wrapperFor(), initialProps: { snapshot: { ...settled, items } },
		});
		await waitFor(() => expect(result.current?.text).toBe("Explain the fix."));
		const firstId = result.current?.id;
		rerender({ snapshot: { ...settled, items: [message("old", "user", "Changed discarded context"), ...items.slice(1, 7),
			message("user", "user", `${"😀".repeat(2048)}changed user ending`),
			message("assistant", "assistant", `changed assistant beginning${"😀".repeat(2048)}`)] } });
		expect(result.current?.id).toBe(firstId);
		expect(post).toHaveBeenCalledOnce();
		rerender({ snapshot: { ...settled, items: [...items.slice(0, -1), message("assistant", "assistant", `prefix${"😀".repeat(2047)}!`)] } });
		expect(result.current).toBeUndefined();
		await waitFor(() => expect(result.current?.text).toBe("Explain the fix."));
		expect(result.current?.id).not.toBe(firstId);
		expect(post).toHaveBeenCalledTimes(2);
	});

	it("requests once per settled context and reuses it when the chat returns", async () => {
		const wrapper = wrapperFor();
		const first = renderHook(() => useConversationPromptSuggestion(settled, true), { wrapper });
		await waitFor(() => expect(first.result.current?.text).toBe("Explain the fix."));
		expect(post).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/conversation/prompt-suggestion", {
			params: { path: { sessionId: "session-1" } }, body: { turnId: "turn-1" }, signal: expect.any(AbortSignal),
		});
		first.rerender();
		first.unmount();
		const second = renderHook(() => useConversationPromptSuggestion(settled, true), { wrapper });
		expect(second.result.current?.text).toBe("Explain the fix.");
		expect(post).toHaveBeenCalledTimes(1);
	});

	it.each<ConversationSnapshot>([
		{ ...settled, turns: [] },
		{ ...settled, controller: { state: "busy" } },
		{ ...settled, mode: "tui" },
		{ ...settled, turns: [...settled.turns, { id: "turn-2", state: "failed", requestedAt: "" }] },
		{ ...settled, turns: [...settled.turns, { id: "turn-2", state: "queued", requestedAt: "" }] },
		{ ...settled, turns: [...settled.turns, { id: "turn-2", state: "running", requestedAt: "" }] },
		{ ...settled, turns: [{ ...settled.turns[0], rolledBack: true }] },
		{ ...settled, items: [{ kind: "message", id: "reply-1", role: "assistant", origin: "provider", turnId: "turn-1", sequence: 1, revision: 1, text: "Working", streaming: true, createdAt: "" }] },
	])("omits suggestions before a settled active conversation is available %#", (snapshot) => {
		const { result } = renderHook(() => useConversationPromptSuggestion(snapshot, true), { wrapper: wrapperFor() });
		expect(result.current).toBeUndefined();
		expect(post).not.toHaveBeenCalled();
	});

	it("selects the latest visible turn after rollback", async () => {
		const snapshot: ConversationSnapshot = {
			...settled,
			turns: [...settled.turns, { id: "discarded", state: "completed", rolledBack: true, requestedAt: "" }],
		};
		const { result } = renderHook(() => useConversationPromptSuggestion(snapshot, true), { wrapper: wrapperFor() });
		await waitFor(() => expect(result.current?.text).toBe("Explain the fix."));
		expect(post.mock.calls[0][1].body).toEqual({ turnId: "turn-1" });
	});

	it("does not request an inactive chat and cancels generation when it becomes inactive", async () => {
		let signal: AbortSignal | undefined;
		post.mockImplementation((_path, options: { signal: AbortSignal }) => {
			signal = options.signal;
			return new Promise(() => {});
		});
		const { result, rerender } = renderHook(({ enabled }) => useConversationPromptSuggestion(settled, enabled), {
			wrapper: wrapperFor(), initialProps: { enabled: false },
		});
		expect(post).not.toHaveBeenCalled();
		rerender({ enabled: true });
		await waitFor(() => expect(post).toHaveBeenCalledOnce());
		rerender({ enabled: false });
		expect(signal?.aborted).toBe(true);
		expect(result.current).toBeUndefined();
	});

	it("cancels an obsolete turn and uses the owning remote daemon", async () => {
		let resolveFirst!: (value: unknown) => void;
		let firstSignal: AbortSignal | undefined;
		post.mockImplementationOnce((_path, options: { signal: AbortSignal }) => {
			firstSignal = options.signal;
			return new Promise((resolve) => { resolveFirst = resolve; });
		}).mockResolvedValue({ data: { turnId: "turn-2", prompt: "Review the tests." } });
		const { result, rerender } = renderHook(({ snapshot }) => useConversationPromptSuggestion(snapshot, true, "host-2"), {
			wrapper: wrapperFor(), initialProps: { snapshot: settled },
		});
		await waitFor(() => expect(post).toHaveBeenCalledOnce());
		rerender({ snapshot: { ...settled, turns: [...settled.turns, { id: "turn-2", state: "completed", requestedAt: "" }] } });
		expect(firstSignal?.aborted).toBe(true);
		await waitFor(() => expect(result.current?.text).toBe("Review the tests."));
		await act(async () => resolveFirst({ data: { turnId: "turn-1", prompt: "Obsolete." } }));
		expect(result.current?.text).toBe("Review the tests.");
		expect(clientForSessionHost).toHaveBeenCalledWith("host-2");
	});

	it("keeps sessions, conversations, branches, and hosts in separate suggestion contexts", async () => {
		const { result, rerender } = renderHook(({ snapshot, hostId }) => useConversationPromptSuggestion(snapshot, true, hostId), {
			wrapper: wrapperFor(), initialProps: { snapshot: settled, hostId: "host-1" },
		});
		await waitFor(() => expect(result.current?.text).toBe("Explain the fix."));
		const ids = new Set([result.current?.id]);
		for (const props of [
			{ snapshot: { ...settled, sessionId: "session-2" }, hostId: "host-1" },
			{ snapshot: { ...settled, conversationId: "conversation-2" }, hostId: "host-1" },
			{ snapshot: { ...settled, activeBranchId: "branch-2" }, hostId: "host-1" },
			{ snapshot: settled, hostId: "host-2" },
		]) {
			rerender(props);
			expect(result.current).toBeUndefined();
			await waitFor(() => expect(result.current?.text).toBe("Explain the fix."));
			ids.add(result.current?.id);
		}
		expect(post).toHaveBeenCalledTimes(5);
		expect(ids.size).toBe(5);
	});

	it.each([
		{ error: { message: "No model configured" } },
		{ data: { turnId: "turn-1", prompt: " " } },
		{ data: { turnId: "other-turn", prompt: "Wrong context." } },
		new Error("Disconnected"),
	])("quietly caches failures or empty suggestions without retries %#", async (response) => {
		if (response instanceof Error) post.mockRejectedValue(response);
		else post.mockResolvedValue(response);
		const client = new QueryClient();
		const wrapper = wrapperFor(client);
		const first = renderHook(() => useConversationPromptSuggestion(settled, true), { wrapper });
		await waitFor(() => expect(client.isFetching()).toBe(0));
		expect(first.result.current).toBeUndefined();
		first.unmount();
		const second = renderHook(() => useConversationPromptSuggestion(settled, true), { wrapper });
		expect(second.result.current).toBeUndefined();
		expect(post).toHaveBeenCalledOnce();
	});
});
