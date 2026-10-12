export type LitmusChatCommand = {
	/** What the daemon-side resolver takes: a PR URL as typed, or bare digits. */
	ref: string;
	/** The pull request number, for the status line. */
	number: number;
};

const PR_URL = "https?:\\/\\/[^\\s/]+\\/[^\\s/]+\\/[^\\s/]+\\/pull\\/(\\d+)(?:[/?#]\\S*)?";
const TEST_PR_NUMBER = /^test\s+pr\s+#?(\d+)$/i;
const TEST_URL = new RegExp(`^test\\s+(${PR_URL})$`, "i");
const LITMUS_NUMBER = /^litmus\s+#?(\d+)$/i;
const LITMUS_URL = new RegExp(`^litmus\\s+(${PR_URL})$`, "i");

/**
 * Matches only the whole message: "test PR #N", "test PR N", "test <PR url>",
 * "litmus <PR url or number>". Anything else, including "test prs please" or
 * "test PR" alone, is an ordinary message for the agent.
 */
export function matchLitmusChatCommand(text: string): LitmusChatCommand | null {
	const value = text.trim();
	for (const pattern of [TEST_PR_NUMBER, LITMUS_NUMBER]) {
		const match = pattern.exec(value);
		if (match?.[1]) return { ref: match[1], number: Number(match[1]) };
	}
	for (const pattern of [TEST_URL, LITMUS_URL]) {
		const match = pattern.exec(value);
		if (match?.[1] && match[2]) return { ref: match[1], number: Number(match[2]) };
	}
	return null;
}
