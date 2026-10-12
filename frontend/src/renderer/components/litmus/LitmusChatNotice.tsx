import { AlertTriangle, FlaskConical, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { LitmusChatNotice as Notice } from "../../hooks/useLitmusChatCommand";

/** Local status line for a "test PR #N" chat command; it is not part of the conversation. */
export function LitmusChatNotice({
	notice,
	projectId,
	onOpenLitmus,
	onDismiss,
}: {
	notice: Notice;
	projectId: string;
	onOpenLitmus?: (projectId: string) => void;
	onDismiss: () => void;
}) {
	const { t } = useTranslation();
	const started = notice.kind === "started";
	return (
		<div className="pointer-events-none absolute inset-x-3 top-3 z-20 flex justify-center" data-testid="litmus-chat-notice">
			<div
				className="pointer-events-auto flex w-full max-w-lg items-center gap-3 rounded-md border border-border bg-surface px-3 py-2 text-sm"
				role={started ? "status" : "alert"}
			>
				{started ? (
					<FlaskConical aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
				) : (
					<AlertTriangle aria-hidden="true" className="size-4 shrink-0 text-warning" />
				)}
				<span className="min-w-0 flex-1 text-pretty text-foreground">
					{started ? t("litmus.chat.started", { number: notice.number, session: notice.sessionId }) : notice.message}
				</span>
				{started ? (
					<a
						className="shrink-0 text-xs text-foreground underline underline-offset-2"
						href={`/projects/${encodeURIComponent(projectId)}/litmus`}
						onClick={(event) => {
							event.preventDefault();
							onOpenLitmus?.(projectId);
						}}
					>
						{t("litmus.chat.open")}
					</a>
				) : null}
				<button
					aria-label={t("common.close")}
					className="grid size-6 shrink-0 place-items-center rounded-md text-muted-foreground hover:bg-interactive-hover hover:text-foreground"
					onClick={onDismiss}
					type="button"
				>
					<X aria-hidden="true" className="size-icon-sm" />
				</button>
			</div>
		</div>
	);
}
