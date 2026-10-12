import { useCallback, useState } from "react";
import { useTranslation } from "react-i18next";
import { matchLitmusChatCommand } from "../components/litmus/litmus-chat-command";
import { resolvePrInput } from "../components/litmus/litmus-format";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { isOrchestratorSession, type WorkspaceSession } from "../types/workspace";
import { useStartLitmusRun } from "./useLitmus";

export type LitmusChatNotice =
	| { kind: "started"; number: number; sessionId: string }
	| { kind: "error"; message: string };

/**
 * "test PR #N" typed in a project's orchestrator chat starts a Litmus run
 * instead of reaching the agent. `intercept` resolves true when it handled the
 * message (started or failed); false means send it to the agent as usual.
 * Worker chats, remote hosts and messages with attachments never intercept.
 */
export function useLitmusChatCommand(session: WorkspaceSession, hostId?: string) {
	const { t } = useTranslation();
	const start = useStartLitmusRun(session.workspaceId);
	const [notice, setNotice] = useState<LitmusChatNotice | null>(null);
	const enabled = isOrchestratorSession(session) && !hostId && !session.hostId;
	const projectId = session.workspaceId;
	const { mutateAsync } = start;

	const intercept = useCallback(
		async (text: string, hasExtras: boolean): Promise<boolean> => {
			if (!enabled || hasExtras) return false;
			const command = matchLitmusChatCommand(text);
			if (!command) return false;
			try {
				let prUrl: string | null = command.ref;
				if (/^\d+$/.test(command.ref)) {
					// The daemon needs a repo; a bare number means the project's own.
					const { data } = await apiClient.GET("/api/v1/testing/runs", {
						params: { query: { projectId, limit: 1 } },
					});
					prUrl = resolvePrInput(command.ref, data?.repo);
				}
				if (!prUrl) {
					setNotice({ kind: "error", message: t("litmus.start.needsUrl") });
					return true;
				}
				const started = await mutateAsync(prUrl);
				if (!started) throw new Error(t("litmus.chat.failed"));
				setNotice({ kind: "started", number: command.number, sessionId: started.sessionId });
			} catch (error) {
				setNotice({ kind: "error", message: apiErrorMessage(error, t("litmus.chat.failed")) });
			}
			return true;
		},
		[enabled, mutateAsync, projectId, t],
	);

	return { intercept, notice, dismiss: useCallback(() => setNotice(null), []) };
}
