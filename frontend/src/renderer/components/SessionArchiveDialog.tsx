import type { ReactElement } from "react";
import { useTranslation } from "react-i18next";
import { sessionArtifacts, type WorkspaceSession } from "../types/workspace";
import { ConfirmDialog } from "./ConfirmDialog";

export type SessionArchiveChoice = { deleteArtifacts: boolean };

// How many artifact names the dialog lists before summarizing the rest.
const LISTED_ARTIFACTS = 3;

// Archiving is reversible — the session moves to the board's Archive section and
// can be restored from there — so this reuses the shared confirm modal without
// the destructive red fill rather than the old inline popover, which read like a
// delete prompt. Callers keep driving `open` themselves: the trigger is rendered
// as-is so repeated taps re-open the confirm instead of toggling it shut.
//
// A local session with artifact files also offers to delete them. Keeping them
// stays the primary action; deleting is the quieter, red secondary one, since
// it cannot be undone and a restored session comes back without them.
export function SessionArchiveDialog({
	onConfirm,
	onOpenChange,
	open,
	session,
	trigger,
}: {
	onConfirm: (choice: SessionArchiveChoice) => void;
	onOpenChange: (open: boolean) => void;
	open: boolean;
	session?: WorkspaceSession;
	trigger: ReactElement;
}) {
	const { t } = useTranslation();
	// A cloud session's teardown is restorable too — the control plane keeps its
	// conversation and work so it can be re-provisioned later.
	const isCloud = session?.cloud !== undefined;
	const body = isCloud ? t("archive.bodyCloud") : t("archive.body");
	const artifacts = session && !isCloud ? sessionArtifacts(session) : [];
	if (artifacts.length === 0) {
		return (
			<>
				{trigger}
				<ConfirmDialog
					cancelLabel={t("common.no")}
					confirmAriaLabel={t("archive.confirmAria")}
					confirmLabel={t("confirm.confirm")}
					description={body}
					onConfirm={() => onConfirm({ deleteArtifacts: false })}
					onOpenChange={onOpenChange}
					open={open}
					title={t("archive.dialog")}
				/>
			</>
		);
	}
	const listed = artifacts.slice(0, LISTED_ARTIFACTS).map((artifact) => artifact.name).join(", ");
	const names =
		artifacts.length > LISTED_ARTIFACTS
			? t("archive.artifactsMore", { names: listed, count: artifacts.length - LISTED_ARTIFACTS })
			: listed;
	return (
		<>
			{trigger}
			<ConfirmDialog
				cancelLabel={t("confirm.cancel")}
				confirmLabel={t("archive.keepArtifacts")}
				description={
					<>
						<p>{body}</p>
						<p className="mt-2">
							{t("archive.artifacts", { count: artifacts.length, names })}
							{session?.outputType === "pr_artifact" ? ` ${t("archive.prUnaffected")}` : null}
						</p>
					</>
				}
				onConfirm={() => onConfirm({ deleteArtifacts: false })}
				onOpenChange={onOpenChange}
				open={open}
				secondaryAction={{
					ariaLabel: t("archive.deleteArtifactsAria"),
					destructive: true,
					label: t("archive.deleteArtifacts"),
					onClick: () => onConfirm({ deleteArtifacts: true }),
				}}
				title={t("archive.dialog")}
			/>
		</>
	);
}
