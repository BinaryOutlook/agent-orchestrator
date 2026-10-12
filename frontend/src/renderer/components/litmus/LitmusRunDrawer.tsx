import { ExternalLink } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
	useDiscardLitmusRun,
	useLitmusRun,
	usePostLitmusRun,
	type LitmusCheck,
} from "../../hooks/useLitmus";
import { AppLink } from "../AppLink";
import { ChatMarkdown } from "../chat/ChatMarkdown";
import { Button } from "../ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "../ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";
import { formatDuration, formatStarted, litmusStatus } from "./litmus-format";
import { LitmusStatusBadge } from "./LitmusStatusBadge";

export function LitmusRunDrawer({
	projectId,
	runId,
	onClose,
}: {
	projectId: string;
	runId: string | null;
	onClose: () => void;
}) {
	const { t, i18n } = useTranslation();
	const query = useLitmusRun(runId);
	const post = usePostLitmusRun(projectId);
	const discard = useDiscardLitmusRun(projectId);
	const run = query.data;
	const awaiting = run?.state === "awaiting-approval";
	const busy = post.isPending || discard.isPending;
	const actionError = post.error ?? discard.error;

	return (
		<Sheet open={runId !== null} onOpenChange={(open) => { if (!open) onClose(); }}>
			<SheetContent className="w-full sm:max-w-2xl" data-testid="litmus-run-drawer">
				<SheetHeader className="border-b border-border pr-12">
					<SheetTitle>{run ? t("litmus.drawer.title", { number: run.pr.number, title: run.pr.title ?? run.repo }) : t("litmus.drawer.titleLoading")}</SheetTitle>
					<SheetDescription>{run ? `${run.repo} · ${formatStarted(run.startedAt, i18n.language)} · ${formatDuration(run.durationSeconds)}` : t("litmus.loading")}</SheetDescription>
				</SheetHeader>
				<div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-4 pb-4">
					{query.isLoading ? <p role="status" className="text-sm text-muted-foreground">{t("litmus.loading")}</p> : null}
					{query.error ? <p role="alert" className="text-sm text-destructive">{query.error.message}</p> : null}
					{run ? (
						<>
							<div className="flex flex-wrap items-center gap-3 text-xs">
								<LitmusStatusBadge status={litmusStatus(run)} />
								<AppLink href={run.pr.url} className="inline-flex items-center gap-1 text-foreground underline-offset-2 hover:underline">
									{t("litmus.drawer.openPr")}<ExternalLink className="size-3" aria-hidden="true" />
								</AppLink>
								{run.commentUrl ? (
									<AppLink href={run.commentUrl} className="inline-flex items-center gap-1 text-foreground underline-offset-2 hover:underline">
										{t("litmus.drawer.openComment")}<ExternalLink className="size-3" aria-hidden="true" />
									</AppLink>
								) : null}
							</div>
							{run.error ? <p role="alert" className="rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">{run.error}</p> : null}
							{awaiting ? (
								<div className="flex flex-wrap items-center gap-2 rounded-md border border-border p-3" data-testid="litmus-approval">
									<p className="mr-auto text-xs text-muted-foreground">{t("litmus.drawer.awaitingHint")}</p>
									<Button size="sm" variant="primary" disabled={busy} onClick={() => post.mutate(run.id)}>{t("litmus.drawer.post")}</Button>
									<Button size="sm" variant="outline" disabled={busy} onClick={() => discard.mutate(run.id)}>{t("litmus.drawer.discard")}</Button>
								</div>
							) : null}
							{actionError ? <p role="alert" className="text-xs text-destructive">{actionError.message}</p> : null}
							<section aria-labelledby="litmus-comment-heading">
								<h3 id="litmus-comment-heading" className="mb-2 text-xs font-medium text-muted-foreground">{t("litmus.drawer.comment")}</h3>
								{run.commentMd.trim() === "" ? (
									<p className="text-sm text-muted-foreground">{t("litmus.drawer.noComment")}</p>
								) : (
									<div className="rounded-md border border-border p-3" data-testid="litmus-comment-preview"><ChatMarkdown text={run.commentMd} /></div>
								)}
							</section>
							<section aria-labelledby="litmus-checks-heading">
								<h3 id="litmus-checks-heading" className="mb-2 text-xs font-medium text-muted-foreground">{t("litmus.drawer.checks")}</h3>
								<ChecksTable checks={run.checks} />
							</section>
							{run.verdictMd.trim() !== "" ? (
								<details className="rounded-md border border-border">
									<summary className="cursor-pointer px-3 py-2 text-xs font-medium text-muted-foreground">{t("litmus.drawer.verdict")}</summary>
									<pre className="overflow-x-auto whitespace-pre-wrap border-t border-border p-3 text-xs">{run.verdictMd}</pre>
								</details>
							) : null}
						</>
					) : null}
				</div>
			</SheetContent>
		</Sheet>
	);
}

function ChecksTable({ checks }: { checks: LitmusCheck[] }) {
	const { t } = useTranslation();
	if (checks.length === 0) return <p className="text-sm text-muted-foreground">{t("litmus.drawer.noChecks")}</p>;
	return (
		<div className="overflow-hidden rounded-md border border-border">
			<Table>
				<TableHeader>
					<TableRow className="text-xs text-muted-foreground">
						<TableHead>{t("litmus.checks.name")}</TableHead>
						<TableHead>{t("litmus.checks.leg")}</TableHead>
						<TableHead>{t("litmus.checks.state")}</TableHead>
						<TableHead>{t("litmus.checks.note")}</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{checks.map((check) => (
						<TableRow key={`${check.id}-${check.leg ?? ""}`} className="text-xs">
							<TableCell className="whitespace-normal">
								<div className="font-medium">{check.name}</div>
								{check.expect ? <div className="text-muted-foreground">{check.expect}</div> : null}
							</TableCell>
							<TableCell>{check.leg ?? "—"}</TableCell>
							<TableCell>{check.state}</TableCell>
							<TableCell className="whitespace-normal text-muted-foreground">{check.note ?? "—"}</TableCell>
						</TableRow>
					))}
				</TableBody>
			</Table>
		</div>
	);
}
