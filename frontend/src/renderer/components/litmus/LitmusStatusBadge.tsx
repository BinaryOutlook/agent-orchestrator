import { useTranslation } from "react-i18next";
import { Badge } from "../ui/badge";
import type { LitmusStatus } from "./litmus-format";

const variants: Record<LitmusStatus, "neutral" | "outline" | "success" | "warning" | "error"> = {
	pass: "success",
	fail: "error",
	partial: "warning",
	blocked: "warning",
	expired: "neutral",
	none: "outline",
	awaiting: "warning",
	running: "outline",
};

export function LitmusStatusBadge({ status }: { status: LitmusStatus }) {
	const { t } = useTranslation();
	return <Badge variant={variants[status]} data-status={status}>{t(`litmus.status.${status}`)}</Badge>;
}
