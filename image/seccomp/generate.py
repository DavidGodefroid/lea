#!/usr/bin/env python3
"""Builds image/seccomp.json from the pinned moby default and lea's reviewed changes (#100).

  python3 -I image/seccomp/generate.py          writes image/seccomp.json
  python3 -I image/seccomp/generate.py --check  fails if image/seccomp.json is not what it builds

The base is moby/profiles' seccomp/default.json, vendored byte for byte in moby-default.json and
pinned by its sha256 in lea.json. lea.json lists every change made to it, each with the reason
for it: a syscall moby gates or denies that lea's nested rootless Podman needs (allow), a syscall
moby allows that lea refuses (remove), a sub-architecture left out (archMap) and lea's own rules
(rules). Nothing else differs from moby: a hand edit of seccomp.json, a changed base or a change
without a reason fails the check, in the unit tests and in CI.
"""
import copy
import hashlib
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(os.path.dirname(HERE), "seccomp.json")


def fail(msg):
    print("seccomp: " + msg, file=sys.stderr)
    sys.exit(1)


def reason(entry, what):
    why = entry.get("why")
    if not isinstance(why, str) or not why.strip() or "\n" in why:
        fail(what + ": a one-line reason (why) is required")
    return why.strip()


def build():
    with open(os.path.join(HERE, "lea.json"), encoding="utf-8") as f:
        spec = json.load(f)
    with open(os.path.join(HERE, "moby-default.json"), "rb") as f:
        raw = f.read()
    pin = spec["base"]["sha256"]
    got = hashlib.sha256(raw).hexdigest()
    if got != pin:
        fail("moby-default.json is not the pinned base (sha256 %s, lea.json pins %s)" % (got, pin))
    prof = json.loads(raw)

    for a in spec.get("archMap", []):
        reason(a, "archMap " + a.get("architecture", "?"))
        hits = [m for m in prof["archMap"] if m["architecture"] == a["architecture"]]
        if len(hits) != 1:
            fail("archMap: %s is not in the base" % a["architecture"])
        subs = hits[0].get("subArchitectures") or []
        for d in a["drop"]:
            if d not in subs:
                fail("archMap: %s is not a sub-architecture of %s in the base" % (d, a["architecture"]))
        hits[0]["subArchitectures"] = [s for s in subs if s not in a["drop"]]

    # A narrowed rule keeps its names, action and argument tests and adds tests of other arguments:
    # it can only match less, and leaves room for a rule of lea's that no allow rule then overlaps.
    for n in spec.get("narrow", []):
        why = reason(n, "narrow %s" % n.get("base", {}).get("names"))
        base, new = n["base"], copy.deepcopy(n["rule"])
        hits = [i for i, r in enumerate(prof["syscalls"]) if r == base]
        if len(hits) != 1:
            fail("narrow %s: the base has %d such rules, not one" % (base.get("names"), len(hits)))
        old_args = base.get("args") or []
        new_args = new.get("args") or []
        if ({k: v for k, v in new.items() if k != "args"} != {k: v for k, v in base.items() if k != "args"}
                or any(a not in new_args for a in old_args) or len(new_args) <= len(old_args)
                or {a["index"] for a in old_args} & {a["index"] for a in new_args if a not in old_args}):
            fail("narrow %s: the rule must be the base rule with tests of other arguments added" % base.get("names"))
        new["comment"] = "lea: " + why
        prof["syscalls"][hits[0]] = new

    def names_in(rule_filter):
        return {n for r in prof["syscalls"] if rule_filter(r) for n in r["names"]}

    removed = set()
    for r in spec.get("remove", []):
        n = r["name"]
        reason(r, "remove " + n)
        if n not in names_in(lambda x: x["action"] == "SCMP_ACT_ALLOW"):
            fail("remove %s: the base does not allow it" % n)
        removed.add(n)

    allowed = []
    for a in spec.get("allow", []):
        n = a["name"]
        why = reason(a, "allow " + n)
        if n in removed or n in (x[0] for x in allowed):
            fail("allow %s: listed twice" % n)
        if n in prof["syscalls"][0]["names"]:
            fail("allow %s: the base already allows it unconditionally" % n)
        allowed.append((n, why))

    # An allowed name gets one unconditional rule: moby's conditional rules for it (by capability,
    # argument or kernel) would overlap it, and crun refuses overlapping rules.
    gone = removed | {n for n, _ in allowed}
    rules = []
    for r in prof["syscalls"]:
        if r["action"] == "SCMP_ACT_ALLOW" or r["names"] != [] and set(r["names"]) <= gone:
            r = dict(r, names=[n for n in r["names"] if n not in gone])
        elif set(r["names"]) & gone:
            fail("%s: a rule of the base that is not an allow rule names it" % sorted(set(r["names"]) & gone))
        if r["names"]:
            rules.append(r)
    lea = [{"names": [n], "action": "SCMP_ACT_ALLOW", "comment": "lea: " + why} for n, why in allowed]
    for x in spec.get("rules", []):
        why = reason(x, "rule %s" % x.get("rule", {}).get("names"))
        rule = copy.deepcopy(x["rule"])
        rule["comment"] = "lea: " + why
        lea.append(rule)
    prof["syscalls"] = rules[:1] + lea + rules[1:]
    return json.dumps(prof, indent="\t", ensure_ascii=False) + "\n"


def main():
    out = build()
    if sys.argv[1:] == ["--check"]:
        try:
            with open(OUT, encoding="utf-8") as f:
                cur = f.read()
        except OSError:
            cur = None
        if cur != out:
            fail("image/seccomp.json is not what image/seccomp/generate.py builds from lea.json and the "
                 "pinned moby default: change lea.json (with a reason) and run it, never the profile by hand")
        return
    if sys.argv[1:]:
        fail("usage: generate.py [--check]")
    with open(OUT, "w", encoding="utf-8") as f:
        f.write(out)


if __name__ == "__main__":
    main()
