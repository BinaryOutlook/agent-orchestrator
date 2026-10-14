#!/usr/bin/env python3
"""Hold a pending ACP approval past the 15-minute stall watchdog, then approve it.

Usage: approval_wait.py <port> <cdp_port> <session_id> <request_id> <requested_at_iso> <evidence_dir>
"""
import datetime
import json
import os
import subprocess
import sys
import time
import urllib.parse
import urllib.request

port, cdp, sid, request_id, requested_at, ev = sys.argv[1:7]
base = f"http://127.0.0.1:{port}/api/v1"
t0 = datetime.datetime.fromisoformat(requested_at.replace("Z", "+00:00"))
os.makedirs(ev, exist_ok=True)
log = open(f"{ev}/approval-timeline.jsonl", "a")


def note(event, **kw):
    rec = {"t": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"), "event": event, **kw}
    print(json.dumps(rec), flush=True)
    log.write(json.dumps(rec) + "\n")
    log.flush()


def call(path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, method="POST" if body is not None else "GET",
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        raw = r.read()
    d = json.loads(raw) if raw else {}  # resolve returns an empty body
    return d.get("data", d)


def state():
    c = call(f"/sessions/{sid}/conversation")
    approval = next((a for a in c.get("activities", []) if a.get("requestId") == request_id), {})
    turns = [{k: t.get(k) for k in ("id", "state", "completedAt", "failureMessage", "error")} for t in c.get("turns", [])]
    texts = [(m.get("text") or "")[:60] for m in c.get("messages", []) if m.get("role") == "assistant"]
    return {"minutes_since_request": round((datetime.datetime.now(datetime.timezone.utc) - t0).total_seconds() / 60, 2),
            "approval": approval.get("status"), "turns": turns, "controller": c.get("controller"), "assistant": texts[-2:]}


def shot(name):
    out = f"{ev}/{name}.png"
    r = subprocess.run(["node", os.path.expanduser("~/.ao/dev/pr6270-lab/shot.cjs"), cdp, out, f"/sessions/{sid}", "2500"],
                       capture_output=True, text=True, timeout=120)
    note("screenshot", file=out, ok=r.returncode == 0)


for minutes in (1, 5, 10, 14, 15, 15.5, 16, 16.5, 17):
    time.sleep(max(0, (t0 + datetime.timedelta(minutes=minutes) - datetime.datetime.now(datetime.timezone.utc)).total_seconds()))
    note("sample", **state())
    if minutes == 16.5:
        shot("approval-pending-16m30s")

time.sleep(max(0, (t0 + datetime.timedelta(minutes=17.5) - datetime.datetime.now(datetime.timezone.utc)).total_seconds()))
quoted = urllib.parse.quote(request_id, safe="")
note("resolve", response=call(f"/sessions/{sid}/conversation/approvals/{quoted}/resolve", {"decisionId": "allow-once"}))
deadline = time.time() + 240
while time.time() < deadline:
    s = state()
    if s["turns"] and s["turns"][-1]["state"] not in ("running", "requested", "starting"):
        break
    time.sleep(2)
note("after_approval", **state())
shot("approval-answered")
json.dump(call(f"/sessions/{sid}/conversation"), open(f"{ev}/approval-conversation.json", "w"), indent=1)
note("done")
