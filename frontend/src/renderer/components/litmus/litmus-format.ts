import type { LitmusRunSummary } from "../../hooks/useLitmus";
import { isLitmusRunActive } from "../../hooks/useLitmus";

export type LitmusStatus = LitmusRunSummary["verdict"] | "awaiting" | "running";

/** What the badge shows: an unfinished or unapproved run outranks its verdict. */
export function litmusStatus(run: Pick<LitmusRunSummary, "state" | "verdict">): LitmusStatus {
	if (run.state === "awaiting-approval") return "awaiting";
	if (isLitmusRunActive(run)) return "running";
	return run.verdict;
}

export function formatDuration(totalSeconds: number): string {
	const seconds = Math.max(0, Math.round(totalSeconds));
	if (seconds < 60) return `${seconds}s`;
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
	return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

export function formatStarted(value: string, locale?: string): string {
	const date = new Date(value);
	if (Number.isNaN(date.getTime())) return "-";
	return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(date);
}

/**
 * Turns what the user typed into something the daemon's start route accepts: a
 * PR URL or owner/repo#N goes through as is; a bare number (or #N) is resolved
 * against the project's own repo. Returns null when a bare number cannot be
 * resolved because the project has no GitHub repo.
 */
export function resolvePrInput(input: string, projectRepo: string | undefined): string | null {
	const value = input.trim();
	const bare = /^#?(\d+)$/.exec(value);
	if (!bare) return value === "" ? null : value;
	if (!projectRepo) return null;
	return `https://github.com/${projectRepo}/pull/${bare[1]}`;
}
