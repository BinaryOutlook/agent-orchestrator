import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { LitmusRunDetail, LitmusRunSummary } from "../../hooks/useLitmus";
import { LitmusView } from "./LitmusView";

const mocks = vi.hoisted(() => ({
	runs: { data: [] as unknown[], isLoading: false, error: null as Error | null },
	detail: { data: undefined as unknown, isLoading: false, error: null as Error | null },
	settings: { data: { askBeforePosting: false }, isLoading: false },
	setSettings: vi.fn(),
	start: vi.fn(),
	rerun: vi.fn(),
	post: vi.fn(),
	discard: vi.fn(),
	hideShellTopbar: false,
}));

vi.mock("../../lib/platform", async (importOriginal) => ({
	...await importOriginal<typeof import("../../lib/platform")>(),
	hidesShellTopbar: () => mocks.hideShellTopbar,
}));
vi.mock("../../hooks/useWorkspaceQuery", () => ({ useWorkspaceQuery: () => ({ data: [{ id: "demo", name: "Demo" }] }) }));
vi.mock("../../hooks/useLitmus", async (importOriginal) => ({
	...await importOriginal<typeof import("../../hooks/useLitmus")>(),
	useLitmusRuns: () => mocks.runs,
	useLitmusRun: (id: string | null) => (id ? mocks.detail : { data: undefined, isLoading: false, error: null }),
	useLitmusSettings: () => mocks.settings,
	useSetLitmusSettings: () => ({ mutate: mocks.setSettings, isPending: false, error: null }),
	useStartLitmusRun: () => ({ mutate: mocks.start, isPending: false, error: null }),
	useRerunLitmusRun: () => ({ mutate: mocks.rerun, isPending: false, error: null, variables: undefined }),
	usePostLitmusRun: () => ({ mutate: mocks.post, isPending: false, error: null }),
	useDiscardLitmusRun: () => ({ mutate: mocks.discard, isPending: false, error: null }),
}));

function run(overrides: Partial<LitmusRunSummary> & { id: string }): LitmusRunSummary {
	return {
		sessionId: "s1",
		projectId: "demo",
		repo: "acme/widgets",
		pr: { number: 12, url: "https://github.com/acme/widgets/pull/12", title: "Fix the widget" },
		state: "submitted",
		verdict: "pass",
		startedAt: "2026-10-12T10:00:00Z",
		durationSeconds: 125,
		tokens: 4200,
		...overrides,
	};
}

function detail(overrides: Partial<LitmusRunDetail> = {}) {
	return {
		...run({ id: "run_1" }),
		run: {},
		verdictMd: "# Verdict\nAll good",
		commentMd: "## Litmus result\n\nThe widget **works**.",
		checks: [{ id: "c1", name: "Widget renders", expect: "A widget appears", leg: "head", state: "pass", note: "saw it" }],
		...overrides,
	};
}

function renderView() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(<QueryClientProvider client={client}><LitmusView projectId="demo" /></QueryClientProvider>);
}

describe("LitmusView", () => {
	beforeEach(() => {
		mocks.runs = { data: [], isLoading: false, error: null };
		mocks.detail = { data: undefined, isLoading: false, error: null };
		mocks.settings = { data: { askBeforePosting: false }, isLoading: false };
		mocks.hideShellTopbar = false;
		for (const fn of [mocks.setSettings, mocks.start, mocks.rerun, mocks.post, mocks.discard]) fn.mockReset();
	});

	it("shows an empty state when the project has no runs", () => {
		renderView();
		expect(screen.getByRole("heading", { name: "Litmus" })).toBeInTheDocument();
		expect(screen.getByText("No runs yet")).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
	});

	it("shows a loading state while runs load", () => {
		mocks.runs = { data: [], isLoading: true, error: null };
		renderView();
		expect(screen.getByRole("status")).toHaveTextContent("Loading runs");
		expect(screen.queryByText("No runs yet")).not.toBeInTheDocument();
	});

	it("shows the load error", () => {
		mocks.runs = { data: [], isLoading: false, error: new Error("daemon unreachable") };
		renderView();
		expect(screen.getByRole("alert")).toHaveTextContent("daemon unreachable");
	});

	it("shows the page title only when the shell title is hidden", () => {
		mocks.hideShellTopbar = true;
		renderView();
		expect(screen.getByRole("heading", { name: "Litmus" })).not.toHaveClass("sr-only");
	});

	it("renders one row per run with the matching verdict badge", () => {
		mocks.runs = {
			isLoading: false,
			error: null,
			data: [
				run({ id: "r-pass", verdict: "pass", pr: { number: 1, url: "https://github.com/acme/widgets/pull/1", title: "One" } }),
				run({ id: "r-fail", verdict: "fail", pr: { number: 2, url: "https://github.com/acme/widgets/pull/2", title: "Two" } }),
				run({ id: "r-partial", verdict: "partial", pr: { number: 3, url: "https://github.com/acme/widgets/pull/3", title: "Three" } }),
				run({ id: "r-blocked", verdict: "blocked", pr: { number: 4, url: "https://github.com/acme/widgets/pull/4", title: "Four" } }),
				run({ id: "r-expired", verdict: "expired", state: "expired", pr: { number: 5, url: "https://github.com/acme/widgets/pull/5", title: "Five" } }),
				run({ id: "r-await", verdict: "pass", state: "awaiting-approval", pr: { number: 6, url: "https://github.com/acme/widgets/pull/6", title: "Six" } }),
				run({ id: "r-running", verdict: "none", state: "running", pr: { number: 7, url: "https://github.com/acme/widgets/pull/7", title: "Seven" } }),
			],
		};
		renderView();
		const expected: Record<string, string> = {
			"r-pass": "Pass",
			"r-fail": "Fail",
			"r-partial": "Partial",
			"r-blocked": "Blocked",
			"r-expired": "Expired",
			"r-await": "Awaiting approval",
			"r-running": "Running",
		};
		for (const [id, label] of Object.entries(expected)) {
			expect(within(screen.getByTestId(`litmus-run-${id}`)).getByText(label)).toBeInTheDocument();
		}
		const row = screen.getByTestId("litmus-run-r-pass");
		expect(within(row).getByRole("link", { name: "#1" })).toHaveAttribute("href", "https://github.com/acme/widgets/pull/1");
		expect(within(row).getByText("2m 5s")).toBeInTheDocument();
		expect(within(row).getByText("4,200")).toBeInTheDocument();
	});

	it("links the posted comment and reruns a finished run", async () => {
		mocks.runs = {
			isLoading: false,
			error: null,
			data: [run({ id: "r1", commentUrl: "https://github.com/acme/widgets/pull/12#issuecomment-1", commentState: "posted" }), run({ id: "r2", state: "running", verdict: "none" })],
		};
		renderView();
		const row = screen.getByTestId("litmus-run-r1");
		expect(within(row).getByRole("link", { name: "View comment" })).toHaveAttribute("href", "https://github.com/acme/widgets/pull/12#issuecomment-1");
		await userEvent.click(within(row).getByRole("button", { name: "Rerun PR #12" }));
		expect(mocks.rerun).toHaveBeenCalledWith("r1");
		expect(within(screen.getByTestId("litmus-run-r2")).getByRole("button", { name: "Rerun PR #12" })).toBeDisabled();
	});

	it("starts a run from a PR URL and from a bare number using the latest run's repo", async () => {
		const user = userEvent.setup();
		mocks.runs = { isLoading: false, error: null, data: [run({ id: "r1" })] };
		renderView();
		const input = screen.getByRole("textbox", { name: "Test a PR" });
		await user.type(input, "34");
		await user.click(screen.getByRole("button", { name: "Run" }));
		expect(mocks.start).toHaveBeenCalledWith("https://github.com/acme/widgets/pull/34", expect.anything());
		await user.clear(input);
		await user.type(input, "https://github.com/acme/widgets/pull/35");
		await user.click(screen.getByRole("button", { name: "Run" }));
		expect(mocks.start).toHaveBeenLastCalledWith("https://github.com/acme/widgets/pull/35", expect.anything());
	});

	it("asks for a full URL when a bare number cannot be resolved", async () => {
		const user = userEvent.setup();
		renderView();
		await user.type(screen.getByRole("textbox", { name: "Test a PR" }), "34");
		await user.click(screen.getByRole("button", { name: "Run" }));
		expect(mocks.start).not.toHaveBeenCalled();
		expect(screen.getByRole("alert")).toHaveTextContent("Enter the full PR URL");
	});

	it("reflects and toggles Ask before posting", async () => {
		mocks.settings = { data: { askBeforePosting: true }, isLoading: false };
		renderView();
		const toggle = screen.getByRole("switch", { name: "Ask before posting" });
		expect(toggle).toBeChecked();
		await userEvent.click(toggle);
		expect(mocks.setSettings).toHaveBeenCalledWith({ askBeforePosting: false });
	});

	it("opens the drawer with the rendered comment and checks", async () => {
		mocks.runs = { isLoading: false, error: null, data: [run({ id: "run_1" })] };
		mocks.detail = { data: detail(), isLoading: false, error: null };
		renderView();
		await userEvent.click(screen.getByRole("button", { name: "Fix the widget" }));
		const drawer = await screen.findByRole("dialog");
		expect(within(drawer).getByRole("heading", { name: "Litmus result" })).toBeInTheDocument();
		expect(within(drawer).getByText("works").tagName).toBe("STRONG");
		expect(within(drawer).getByText("Widget renders")).toBeInTheDocument();
		expect(within(drawer).getByText("saw it")).toBeInTheDocument();
		expect(within(drawer).queryByRole("button", { name: "Post comment" })).not.toBeInTheDocument();
	});

	it("offers Post and Discard for a run awaiting approval", async () => {
		const user = userEvent.setup();
		mocks.runs = { isLoading: false, error: null, data: [run({ id: "run_1", state: "awaiting-approval" })] };
		mocks.detail = { data: detail({ state: "awaiting-approval", commentState: "awaiting-approval" }), isLoading: false, error: null };
		renderView();
		await user.click(screen.getByRole("button", { name: "Fix the widget" }));
		const drawer = await screen.findByRole("dialog");
		await user.click(within(drawer).getByRole("button", { name: "Post comment" }));
		expect(mocks.post).toHaveBeenCalledWith("run_1");
		await user.click(within(drawer).getByRole("button", { name: "Discard" }));
		expect(mocks.discard).toHaveBeenCalledWith("run_1");
	});
});
