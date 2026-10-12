import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { clientForSessionHost } from "../lib/host-clients";
import type { ConversationSnapshot } from "../types/conversation";

export function useConversationPromptSuggestion(
	snapshot: ConversationSnapshot | undefined,
	enabled: boolean,
	hostId?: string,
): { id: string; text: string } | undefined {
	// Match the daemon's bounded promptSuggestionContext so replay metadata cannot regenerate hints.
	const sourceContext = useMemo(() => {
		const recent: Array<[string, string]> = [];
		const items = snapshot?.items ?? [];
		for (let i = items.length - 1; i >= 0 && recent.length < 8; i--) {
			const item = items[i];
			if (item.kind !== "message") continue;
			let text = item.text.trim();
			if (!text) continue;
			const runes = Array.from(text);
			if (runes.length > 2048) text = item.role === "assistant"
				? `…${runes.slice(-2048).join("")}` : `${runes.slice(0, 2048).join("")}…`;
			recent.push([item.role, text]);
		}
		return JSON.stringify(recent.reverse());
	}, [snapshot?.items]);
	const latestTurn = snapshot?.turns.filter((turn) => !turn.rolledBack).at(-1);
	const turnId = enabled && snapshot?.mode === "chat" && snapshot.controller.state === "ready" &&
		latestTurn?.state === "completed" &&
		!snapshot.turns.some((turn) => turn.state === "running" || turn.state === "queued") &&
		!snapshot.items.some((item) => item.kind === "message" && item.streaming)
		? latestTurn.id : undefined;
	const sessionId = snapshot?.sessionId ?? "";
	const contextKey = [hostId ?? "local", sessionId, snapshot?.conversationId ?? "", snapshot?.activeBranchId ?? "", turnId ?? "", sourceContext];
	const query = useQuery({
		queryKey: ["conversation-prompt-suggestion", ...contextKey],
		enabled: Boolean(turnId),
		staleTime: Infinity,
		gcTime: Infinity,
		retry: false,
		queryFn: async ({ signal }) => {
			if (!turnId) return null;
			try {
				const { data, error } = await clientForSessionHost(hostId).POST(
					"/api/v1/sessions/{sessionId}/conversation/prompt-suggestion",
					{ params: { path: { sessionId } }, body: { turnId }, signal },
				);
				if (error || data?.turnId !== turnId || !data.prompt.trim()) return null;
				return { id: JSON.stringify(contextKey), text: data.prompt.trim() };
			} catch (error) {
				if (signal.aborted) throw error;
				// Cache an omitted suggestion too, so returning to a turn never retries generation.
				return null;
			}
		},
	});
	return turnId ? query.data ?? undefined : undefined;
}
