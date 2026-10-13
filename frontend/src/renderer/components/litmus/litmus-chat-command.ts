export type LitmusChatCommand = {
	/** What the daemon-side resolver takes: a PR URL as typed, or bare digits. */
	ref: string;
	/** The pull request number, for the status line. */
	number: number;
};

const PR_URL = "https?:\\/\\/[^\\s/]+\\/[^\\s/]+\\/[^\\s/]+\\/pull\\/(\\d+)(?:[/?#]\\S*?)?";
const LEAD = "(?:(?:please|pls)\\s+)?";
const TAIL = "(?:\\s+(?:please|pls))?[.!]?$";
const TEST_PR_NUMBER = new RegExp(`^${LEAD}test\\s+(?:(?:this|the)\\s+)?pr\\s+#?(\\d+)${TAIL}`, "i");
const TEST_URL = new RegExp(`^${LEAD}test\\s+(${PR_URL})${TAIL}`, "i");
const LITMUS_NUMBER = new RegExp(`^${LEAD}litmus\\s+#?(\\d+)${TAIL}`, "i");
const TEST_BARE_NUMBER = new RegExp(`^${LEAD}test\\s+#?(\\d+)${TAIL}`, "i");
const NATURAL_MAX_LENGTH = 160;
const NATURAL_URL = /https?:\/\/[^\s/]+\/[^\s/]+\/[^\s/]+\/pull\/(\d+)\S*/gi;
// The only numbers a sentence may carry: "PR 12", "PR #12", "pull request 12", "number is 12", "PR number 12".
const NATURAL_PR_NUMBER = /(?:\bpr\s*#?|\bpull\s+request\s+#?|\bnumber\s+(?:is\s+)?(?:pr\s+)?#?)(\d+)\b/i;
// An imperative start: an optional "please"/"pls", an optional "can/could/would you (please)", then the command.
const NATURAL_START = /^(?:(?:please|pls)[,\s]+)?(?:(?:can|could|would)\s+you\s+(?:(?:please|pls)[,\s]+)?)?(?:test|run\s+litmus\s+on|litmus)\b/i;
const NATURAL_OTHER_INTENT =
	/\b(?:fix|review|write|add|why|explain|merge|revert|close|update|refactor|debug|investigate|remove|flaky|failures?|helper|cover|since|suite)\b/i;
const LITMUS_URL = new RegExp(`^${LEAD}litmus\\s+(${PR_URL})${TAIL}`, "i");

/**
 * Free-form phrasing: one line of at most 160 characters that starts with an
 * imperative ("test", "run litmus on" or "litmus", after an optional
 * please/pls and "can/could/would you"), none of fix/review/write/add/why/
 * explain/merge/revert/close/update/refactor/debug/investigate/remove/flaky/
 * failure(s)/helper/cover/since/suite, and exactly one PR-tagged reference (a
 * pull URL, "PR N", "PR #N", "pull request N", "number (is) N" or "PR number
 * N"). A bare "#N" or "N" matches only in the short forms below; any other
 * number or a second URL makes it an ordinary message.
 */
function matchNaturalLitmusCommand(value: string): LitmusChatCommand | null {
	if (value.length > NATURAL_MAX_LENGTH || /[\r\n]/.test(value)) return null;
	if (!NATURAL_START.test(value) || NATURAL_OTHER_INTENT.test(value)) return null;
	const urls = [...value.matchAll(NATURAL_URL)];
	const rest = value.replace(NATURAL_URL, " ");
	const stray = rest.match(/\d+/g) ?? [];
	if (urls.length > 1 || urls.length + stray.length !== 1) return null;
	const url = urls[0];
	if (url?.[1]) return { ref: url[0].replace(/[.,;:!?)\]>"']+$/, ""), number: Number(url[1]) };
	const digits = NATURAL_PR_NUMBER.exec(rest)?.[1];
	return digits ? { ref: digits, number: Number(digits) } : null;
}

/**
 * Matches the whole message, case-insensitive: "test PR #N", "test PR N",
 * "test this PR #N" / "test the PR N", "test <PR url>", "test N", "litmus <PR
 * url or number>", each with an optional leading "please"/"pls" and an
 * optional trailing "please"/"pls", "." or "!". Failing that, an imperative
 * sentence about testing a single PR also matches (see
 * matchNaturalLitmusCommand), e.g. "can you test PR 6588?". Anything else,
 * including "test prs please", "test PR" alone, "fix the failing test in PR
 * 6588" or "run litmus on the flaky suite since PR 6588", is an ordinary
 * message for the agent.
 */
export function matchLitmusChatCommand(text: string): LitmusChatCommand | null {
	const value = text.trim();
	for (const pattern of [TEST_PR_NUMBER, LITMUS_NUMBER, TEST_BARE_NUMBER]) {
		const match = pattern.exec(value);
		if (match?.[1]) return { ref: match[1], number: Number(match[1]) };
	}
	for (const pattern of [TEST_URL, LITMUS_URL]) {
		const match = pattern.exec(value);
		if (match?.[1] && match[2]) return { ref: match[1], number: Number(match[2]) };
	}
	return matchNaturalLitmusCommand(value);
}
