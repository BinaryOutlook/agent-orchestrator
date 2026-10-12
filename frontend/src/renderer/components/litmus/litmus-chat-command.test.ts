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
	])("accepts %j", (text, ref, number) => {
		expect(matchLitmusChatCommand(text)).toEqual({ ref, number });
	});

	it.each([
		"test prs please",
		"test PR",
		"test PR #",
		"test PR #12 please",
		"please test PR #12",
		"test the login flow",
		"test https://example.com/",
		"test https://github.com/acme/app/issues/12",
		"litmus",
		"litmus status",
		"litmus 12 and 13",
		"fix the bug in PR #12",
		"",
	])("rejects %j", (text) => {
		expect(matchLitmusChatCommand(text)).toBeNull();
	});
});
