import { useQueryClient } from "@tanstack/react-query";
import { ChevronDown, Copy, ExternalLink, File, FileText, Folder, Globe, Loader2, Plus, Search } from "lucide-react";
import { ScrollArea } from "radix-ui";
import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useCreateArtifactSession } from "../hooks/useCreateArtifactSession";
import { sessionArtifactFileQueryOptions } from "../hooks/useSessionWorkspaceFiles";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import {
	artifactCount,
	filterArtifactGroups,
	groupArtifactsByProject,
	type ArtifactGroup,
	type ArtifactRow,
} from "../lib/artifacts";
import { aoBridge } from "../lib/bridge";
import { formatTimeCompact } from "../lib/format-time";
import { useNavigateToSession } from "../lib/navigate-to-session";
import { hidesShellTopbar } from "../lib/platform";
import { useUiStore } from "../stores/ui-store";
import { CLOUD_PROJECT_KIND, STANDALONE_WORKSPACE_ID, type WorkspaceSummary } from "../types/workspace";
import { Button } from "./ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

const ALL_PROJECTS = "all";

/** Every artifact agents have written on this machine, grouped by project. */
export function ArtifactsView() {
	const { t } = useTranslation();
	const showPageTitle = hidesShellTopbar();
	const workspaceQuery = useWorkspaceQuery({ includeCloud: false });
	const workspaces = workspaceQuery.data;
	const groups = useMemo(() => groupArtifactsByProject(workspaces ?? []), [workspaces]);
	const [query, setQuery] = useState("");
	const [projectFilter, setProjectFilter] = useState(ALL_PROJECTS);
	const visibleGroups = useMemo(
		() => filterArtifactGroups(groups, query, projectFilter === ALL_PROJECTS ? "" : projectFilter),
		[groups, projectFilter, query],
	);
	const hasArtifacts = artifactCount(groups) > 0;
	const createArtifact = useCreateArtifactSession();
	const clearFilters = () => {
		setQuery("");
		setProjectFilter(ALL_PROJECTS);
	};

	return (
		<div className="flex min-h-0 flex-1 flex-col bg-background">
			<header className="workspace-topbar-container center-panel-titlebar mt-4 flex h-toolbar shrink-0 items-center justify-between gap-3 pr-8">
				<h1 className={showPageTitle ? "truncate pl-3.5 text-heading font-semibold leading-none tracking-tight text-foreground" : "sr-only"}>
					{t("artifacts.title")}
				</h1>
				{hasArtifacts ? (
					<div className="workspace-topbar-actions flex shrink-0 items-center gap-2">
						<div className="relative">
							<Search aria-hidden="true" className="pointer-events-none absolute start-2.5 top-1/2 size-icon-sm -translate-y-1/2 text-muted-foreground" />
							<Input
								aria-label={t("artifacts.search")}
								className="h-control-md w-56 ps-8 text-xs [&::-webkit-search-cancel-button]:appearance-none"
								onChange={(event) => setQuery(event.target.value)}
								placeholder={t("artifacts.search")}
								type="search"
								value={query}
							/>
						</div>
						<Select value={projectFilter} onValueChange={setProjectFilter}>
							<SelectTrigger size="sm" className="h-control-md text-xs" aria-label={t("artifacts.projectFilter")}>
								<SelectValue />
							</SelectTrigger>
							<SelectContent position="popper" align="end">
								<SelectItem value={ALL_PROJECTS}>{t("artifacts.allProjects")}</SelectItem>
								{groups.map((group) => (
									<SelectItem key={group.id} value={group.id}>
										{group.isScratchpad ? t("standalone.workspaceName") : group.name}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<CreateArtifactMenu
							workspaces={workspaces ?? []}
							create={createArtifact.create}
							pending={createArtifact.pending}
							label="new"
							align="end"
							className="h-control-md"
							disabledReason={createArtifact.agentReady ? undefined : t("artifacts.noAgent.description")}
						/>
					</div>
				) : null}
			</header>
			<ScrollArea.Root asChild type="hover" scrollHideDelay={500}>
				<main className="relative min-h-0 flex-1 overflow-hidden">
					<ScrollArea.Viewport className="h-full w-full overscroll-contain focus-visible:outline-none">
						<div className="px-8 pb-8 pt-3">
							{workspaceQuery.isLoading ? (
								<p role="status" className="px-3 py-8 text-center text-sm text-muted-foreground">{t("artifacts.loading")}</p>
							) : workspaceQuery.error ? (
								<p role="alert" className="px-3 py-8 text-center text-sm text-destructive">{workspaceQuery.error.message}</p>
							) : !hasArtifacts ? (
								<EmptyArtifacts workspaces={workspaces ?? []} />
							) : visibleGroups.length === 0 ? (
								<div className="flex flex-col items-center gap-1 py-16 text-center">
									<p className="text-control font-medium text-foreground">{t("artifacts.noMatches")}</p>
									<Button variant="ghost" size="sm" className="text-muted-foreground" onClick={clearFilters}>
										{t("artifacts.clearFilters")}
									</Button>
								</div>
							) : (
								<div className="flex flex-col gap-6">
									{visibleGroups.map((group) => (
										<ArtifactGroupSection key={group.id} group={group} />
									))}
								</div>
							)}
						</div>
					</ScrollArea.Viewport>
					<ScrollArea.Scrollbar orientation="vertical" className="z-10 flex w-2 touch-none select-none p-0.5">
						<ScrollArea.Thumb className="relative flex-1 rounded-full bg-[color-mix(in_srgb,var(--color-scrollbar)_42%,transparent)] hover:bg-[color-mix(in_srgb,var(--color-scrollbar)_56%,transparent)]" />
					</ScrollArea.Scrollbar>
				</main>
			</ScrollArea.Root>
		</div>
	);
}

function ArtifactGroupSection({ group }: { group: ArtifactGroup }) {
	const { t } = useTranslation();
	const name = group.isScratchpad ? t("standalone.workspaceName") : group.name;
	const GroupIcon = group.isScratchpad ? File : Folder;
	return (
		<section aria-label={name}>
			<h2 className="flex h-8 items-center gap-2 px-2.5 text-xs font-medium text-muted-foreground">
				<GroupIcon aria-hidden="true" className="size-icon-sm shrink-0" />
				<span className="truncate text-foreground">{name}</span>
				<span className="tabular-nums text-passive">{group.rows.length}</span>
			</h2>
			<ul className="divide-y divide-border">
				{group.rows.map((row) => (
					<ArtifactListRow key={`${row.sessionId}:${row.path}`} row={row} />
				))}
			</ul>
		</section>
	);
}

function ArtifactListRow({ row }: { row: ArtifactRow }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const requestWorkspaceFileOpen = useUiStore((state) => state.requestWorkspaceFileOpen);
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);
	const navigateToSession = useNavigateToSession();
	const KindIcon = row.kind === "html" ? Globe : row.kind === "markdown" ? FileText : File;
	const externalUrl = row.previewUrl ?? row.rawUrl;

	const open = () => {
		requestWorkspaceFileOpen(row.sessionId, row.path, undefined, "artifact");
		navigateToSession(row.projectId, row.sessionId);
	};
	const copyContents = async () => {
		try {
			const file = await queryClient.fetchQuery(sessionArtifactFileQueryOptions(row.sessionId, row.path, row.rawUrl, t("files.error.loadArtifact")));
			if (file.binary) throw new Error(t("artifacts.copyBinary"));
			await aoBridge.clipboard.writeText(file.content);
			showGlobalToast(t("artifacts.copied", { name: row.name }));
		} catch (error) {
			showGlobalToast(error instanceof Error ? error.message : t("files.error.loadArtifact"), undefined, "error");
		}
	};

	return (
		<li className="group/artifact relative flex h-[38px] items-center gap-2.5 rounded-md px-2.5 transition-colors duration-(--duration-fast) hover:bg-interactive-hover focus-within:bg-interactive-hover motion-reduce:transition-none">
			<button
				type="button"
				className="flex min-w-0 flex-1 items-center gap-2.5 self-stretch rounded-sm text-left outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
				onClick={open}
			>
				{row.generating ? (
					<Loader2 aria-label={t("artifacts.generating")} className="size-icon-sm shrink-0 animate-spin text-status-working motion-reduce:animate-none" />
				) : (
					<KindIcon aria-hidden="true" className="size-icon-sm shrink-0 text-muted-foreground" />
				)}
				<span className="shrink-0 truncate text-control font-medium text-foreground">{row.name}</span>
				<span className="min-w-0 truncate text-xs text-muted-foreground">{row.sessionTitle}</span>
			</button>
			<div className="flex shrink-0 items-center gap-0.5 opacity-0 transition-opacity duration-(--duration-fast) group-hover/artifact:opacity-100 group-focus-within/artifact:opacity-100 motion-reduce:transition-none">
				<RowAction label={t("artifacts.copy")} onClick={() => void copyContents()}>
					<Copy className="size-icon-sm" />
				</RowAction>
				{externalUrl ? (
					<RowAction label={t("artifacts.openExternal")} onClick={() => window.open(externalUrl, "_blank", "noopener,noreferrer")}>
						<ExternalLink className="size-icon-sm" />
					</RowAction>
				) : null}
			</div>
			<time dateTime={row.updatedAt} className="w-20 shrink-0 text-right text-xs tabular-nums text-passive">
				{formatTimeCompact(row.updatedAt)}
			</time>
		</li>
	);
}

function RowAction({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Button variant="ghost" size="icon-sm" className="text-muted-foreground" aria-label={label} onClick={onClick}>
					{children}
				</Button>
			</TooltipTrigger>
			<TooltipContent>{label}</TooltipContent>
		</Tooltip>
	);
}

function EmptyArtifacts({ workspaces }: { workspaces: readonly WorkspaceSummary[] }) {
	const { t } = useTranslation();
	const openGlobalSettings = useUiStore((state) => state.openGlobalSettings);
	const { agentReady, create, pending } = useCreateArtifactSession();

	if (!agentReady) {
		return (
			<div className="flex flex-col items-center gap-1 py-24 text-center">
				<h2 className="text-heading-sm font-semibold text-foreground">{t("artifacts.noAgent.title")}</h2>
				<p className="max-w-72 text-xs text-muted-foreground">{t("artifacts.noAgent.description")}</p>
				<Button variant="ghost" size="sm" className="mt-2" onClick={() => openGlobalSettings("agents")}>
					{t("artifacts.noAgent.action")}
				</Button>
			</div>
		);
	}
	return (
		<div className="flex flex-col items-center gap-1 py-24 text-center">
			<h2 className="text-heading-sm font-semibold text-foreground">{t("artifacts.empty.title")}</h2>
			<p className="max-w-72 text-xs text-muted-foreground">{t("artifacts.empty.description")}</p>
			<CreateArtifactMenu workspaces={workspaces} create={create} pending={pending} className="mt-3" align="center" />
		</div>
	);
}

/**
 * The Create artifact trigger and its Scratchpad / project menu. The empty
 * state and the list header share it so both start the same session.
 */
function CreateArtifactMenu({
	workspaces,
	create,
	pending,
	label = "create",
	align,
	className,
	disabledReason,
}: {
	workspaces: readonly WorkspaceSummary[];
	create: (projectId?: string) => Promise<void>;
	pending: boolean;
	label?: "create" | "new";
	align: "center" | "end";
	className?: string;
	disabledReason?: string;
}) {
	const { t } = useTranslation();
	const projects = workspaces.filter(
		(workspace) => workspace.id !== STANDALONE_WORKSPACE_ID && workspace.kind !== CLOUD_PROJECT_KIND && !workspace.hostId,
	);
	const trigger = (
		<Button size="sm" className={className} disabled={pending || Boolean(disabledReason)} aria-busy={pending}>
			{pending ? (
				<Loader2 aria-hidden="true" className="size-icon-sm animate-spin text-status-working motion-reduce:animate-none" />
			) : label === "new" ? (
				<Plus aria-hidden="true" className="size-icon-sm" />
			) : null}
			{pending ? t("artifacts.creating") : label === "new" ? t("artifacts.new") : t("artifacts.create")}
			{pending || label === "new" ? null : <ChevronDown aria-hidden="true" className="size-icon-sm" />}
		</Button>
	);
	if (disabledReason) {
		return (
			<Tooltip>
				<TooltipTrigger asChild>
					<span tabIndex={0} className="inline-flex rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60">
						{trigger}
					</span>
				</TooltipTrigger>
				<TooltipContent>{disabledReason}</TooltipContent>
			</Tooltip>
		);
	}
	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>{trigger}</DropdownMenuTrigger>
			<DropdownMenuContent align={align} className="min-w-56">
				<DropdownMenuItem onSelect={() => void create()}>
					<File aria-hidden="true" className="size-icon-sm" />
					{t("standalone.workspaceName")}
				</DropdownMenuItem>
				{projects.length > 0 ? <DropdownMenuSeparator /> : null}
				{projects.map((project) => (
					<DropdownMenuItem key={project.id} onSelect={() => void create(project.id)}>
						<Folder aria-hidden="true" className="size-icon-sm" />
						<span className="truncate">{project.name}</span>
					</DropdownMenuItem>
				))}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
