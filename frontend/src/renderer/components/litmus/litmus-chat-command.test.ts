import { describe, expect, it } from "vitest";
import { matchLitmusChatCommand } from "./litmus-chat-command";

const URL = "https://github.com/acme/app/pull/6473";

describe("matchLitmusChatCommand", () => {
	it.each([
		["test PR #6473", "6473", 6473],
		["test PR 6473", "6473", 6473],
		["Test pr #12", "12", 12],
		["  TEST PR 12  ", "12", 12],
		[`test ${URL}`, URL, 6473],
		[`TEST ${URL}`, URL, 6473],
		[`test ${URL}/files`, `${URL}/files`, 6473],
		["litmus 6473", "6473", 6473],
		["litmus #6473", "6473", 6473],
		[`litmus ${URL}`, URL, 6473],
		[`LITMUS ${URL}`, URL, 6473],
		["test pr 6588", "6588", 6588],
		["Test this PR #6588", "6588", 6588],
		["test the pr 6588", "6588", 6588],
		["test PR 6588 please", "6588", 6588],
		["test PR #6588.", "6588", 6588],
		["test PR #6588!", "6588", 6588],
		["Please test PR #6588", "6588", 6588],
		["please test this PR 6588 please.", "6588", 6588],
		["litmus 6588", "6588", 6588],
		["litmus 6588 please", "6588", 6588],
		["please litmus #6588!", "6588", 6588],
		[`test ${URL}.`, URL, 6473],
		[`please test ${URL} please`, URL, 6473],
		[`test ${URL}/files.`, `${URL}/files`, 6473],
		[`litmus ${URL}!`, URL, 6473],
		["Can you please test this PR? Its number is PR 6588.", "6588", 6588],
		["test 6588 pls", "6588", 6588],
		["please run litmus on PR 6588", "6588", 6588],
		["could you test https://github.com/OrchestratorInc/agent-orchestrator/pull/6588 for me", "https://github.com/OrchestratorInc/agent-orchestrator/pull/6588", 6588],
		[`can you test ${URL}?`, URL, 6473],
		["test please PR 12", "12", 12],
		["test PR #12 please now", "12", 12],
		["can you test PR 6588 and tell me", "6588", 6588],
		["can you test PR 6588", "6588", 6588],
		["test PR 6588?", "6588", 6588],
		["test the PR number is 6588", "6588", 6588],
		["test the pull request 6588", "6588", 6588],
		["could you please test PR number 6588", "6588", 6588],
		["Would you test PR #6588?", "6588", 6588],
		["pls test pr 6588", "6588", 6588],
		["test 6588", "6588", 6588],
		["test #6588 please", "6588", 6588],
	])("accepts %j", (text, ref, number) => {
		expect(matchLitmusChatCommand(text)).toEqual({ ref, number });
	});

	it.each([
		"test prs please",
		"test PR",
		"test PR #",
		"test this 12",
		"test PR #12 and #13",
		"please test PR",
		"please",
		"test this PR",
		"test the login flow",
		"test https://example.com/",
		"test https://github.com/acme/app/issues/12",
		"litmus",
		"litmus status",
		"litmus 12 and 13",
		"fix the bug in PR #12",
		"fix the failing test in PR 6588",
		"why does PR 6588 break the test",
		"review and test PR 6588",
		"test 6588 and 6590",
		"test PR 6588 in 5 minutes",
		`test ${URL} and ${URL.replace("6473", "6474")}`,
		`test ${URL} and 12`,
		"test PR 6588\nthen tell me",
		`test PR 6588 ${"x".repeat(160)}`,
		// not an imperative start
		"I tried to test PR 6588 yesterday",
		"the test for PR 6588 is red",
		// bare numbers are not PR references inside a sentence
		"can you test 6588 for me",
		"run litmus on 6588",
		"test the build #12 on PR",
		// wider intent words
		"test the update flow in PR 6588",
		"test PR 6588 and debug the failures",
		"litmus is flaky since PR 6588",
		"run litmus on the suite for PR 6588",
		"test the helper that PR 6588 touches",
		"please test PR 6588 and investigate the failure",
		"test whether PR 6588 will cover the refactor",
		"",
	])("rejects %j", (text) => {
		expect(matchLitmusChatCommand(text)).toBeNull();
	});
});
