import { strictEqual } from "node:assert";
import { describe, it } from "node:test";

import {
  isValidDomain,
  isValidNamespace,
  isValidRegex,
  isValidRule,
  isValidSubnet,
  isValidSubnet6,
  isValidWildcard,
} from "../../src/utils/rule-validators";

describe("Rule validator", () => {
  it("revalidates a cached row after editing its pattern or type", () => {
    const rule = { type: "domain", rule: "example.com" };
    strictEqual(isValidRule(rule), true);
    rule.rule = "[";
    strictEqual(isValidRule(rule), false);
    rule.type = "regex";
    strictEqual(isValidRule(rule), false);
    rule.rule = "^example\\.com$";
    strictEqual(isValidRule(rule), true);
    rule.type = "domain";
    strictEqual(isValidRule(rule), false);
    rule.rule = "example.com";
    strictEqual(isValidRule(rule), true);
  });
  it("validates full IPv4 and IPv6 syntax, including prefixes", () => {
    for (const pattern of [
      "::",
      "::1",
      "2001:db8::1/64",
      "2001:db8:1:2:3:4:5:6/128",
      "::ffff:192.0.2.1",
      "::/0",
    ]) {
      strictEqual(isValidSubnet6(pattern), true, pattern);
    }
    for (const pattern of [
      ":",
      "1::2::3",
      "12345::1",
      "1:2:3:4:5:6:7",
      "1:2:3:4:5:6:7:8::",
      "::1/",
      "::1/+64",
      "::1/064",
      "::1/129",
      "::1/18446744073709551616",
      "fe80::1%eth0",
      "::ffff:999.1.1.1",
      "::1\n",
    ]) {
      strictEqual(isValidSubnet6(pattern), false, pattern);
    }
    for (const pattern of ["0.0.0.0/0", "192.0.2.1", "255.255.255.255/32"]) {
      strictEqual(isValidSubnet(pattern), true, pattern);
    }
    for (const pattern of [
      "256.0.0.1",
      "192.168.001.1",
      "1.2.3.4/33",
      "1.2.3.4/",
      "1.2.3.4/01",
      "1.2.3",
      "1.2.3.4\n",
    ]) {
      strictEqual(isValidSubnet(pattern), false, pattern);
    }
  });

  it("rejects empty rules and unknown types without inherited property lookups", () => {
    for (const type of ["unknown", "toString", "__proto__"]) {
      strictEqual(isValidRule({ type, rule: "example.com" }), false);
    }
    strictEqual(isValidRule({ type: "regex", rule: "" }), false);
    strictEqual(isValidRule({ type: "subnet6", rule: "1::2::3" }), false);
    strictEqual(isValidRule({ type: "domain", rule: "example.com" }), true);
  });
  it("should be valid regexp", () => {
    strictEqual(isValidRegex("^[a-zA-Z0-9]+$"), true);
    strictEqual(isValidRegex("^[a-zA-Z0-9+$"), false);
    strictEqual(isValidRegex(".*"), true);
    strictEqual(isValidRegex("a(b|c)d"), true);
    strictEqual(isValidRegex("\\d+"), true);
  });

  it("should be valid domain", () => {
    strictEqual(isValidDomain("domain.com"), true);
    strictEqual(isValidDomain("domain.com.cn"), true);
    strictEqual(isValidDomain("domain.com.cn.cn"), true);
    strictEqual(isValidDomain(".com.cn.cn.cn"), false);
    strictEqual(isValidDomain("com.cn.cn."), false);
    strictEqual(isValidDomain("sub.domain.com"), true);
    strictEqual(isValidDomain("sub-domain.com"), true);
    strictEqual(isValidDomain("sub.domain-test.com"), true);
    strictEqual(isValidDomain("123.domain.com"), true);
    strictEqual(isValidDomain("domain.123.com"), true);
    strictEqual(isValidDomain("domain.com123"), true);
    strictEqual(isValidDomain("domain.com-"), true);
    strictEqual(isValidDomain("-domain.com"), true);
    strictEqual(isValidDomain("domain.-com"), true);
    strictEqual(isValidDomain("domain..com"), false);
    strictEqual(isValidDomain(".domain"), false);
    strictEqual(isValidDomain("domain.123"), true);
    strictEqual(isValidDomain("domain.123.123"), true);
    strictEqual(isValidDomain("a.b.c.d"), true);
    strictEqual(isValidDomain("a.b.c.123"), true);
    strictEqual(isValidDomain("a.b.c.d-"), true);
    strictEqual(isValidDomain("a.b.c.-d"), true);
    strictEqual(isValidDomain("a.b.c..d"), false);
    strictEqual(isValidDomain("a.b.c."), false);
    strictEqual(isValidDomain(".a.b.c"), false);
  });

  it("should be valid wildcard", () => {
    strictEqual(isValidWildcard("*.domain.com"), true);
    strictEqual(isValidWildcard("*.sub.domain.com"), true);
    strictEqual(isValidWildcard("domain.com"), true);
    strictEqual(isValidWildcard("sub.domain.com"), true);
    strictEqual(isValidWildcard("*.domain.com.cn"), true);
    strictEqual(isValidWildcard("*.sub.domain.com.cn"), true);
    strictEqual(isValidWildcard("domain.com.cn"), true);
    strictEqual(isValidWildcard("sub.domain.com.cn"), true);
    strictEqual(isValidWildcard("*.domain.com.cn.cn"), true);
    strictEqual(isValidWildcard("*.sub.domain.com.cn.cn"), true);
    strictEqual(isValidWildcard("domain.com.cn.cn"), true);
    strictEqual(isValidWildcard("sub.domain.com.cn.cn"), true);
    strictEqual(isValidWildcard("*.domain.123"), true);
    strictEqual(isValidWildcard("domain.123"), true);
    strictEqual(isValidWildcard("*.domain.123.123"), true);
    strictEqual(isValidWildcard("domain.123.123"), true);
    strictEqual(isValidWildcard("*.domain.com-"), true);
    strictEqual(isValidWildcard("*.domain.-com"), true);
    strictEqual(isValidWildcard("*.domain..com"), false);
    strictEqual(isValidWildcard("*.domain"), true);
    strictEqual(isValidWildcard("*.domain."), false);
    strictEqual(isValidWildcard(".*.domain"), false);
    strictEqual(isValidWildcard("*."), false);
    strictEqual(isValidWildcard("domain"), true);
    strictEqual(isValidWildcard("*domain.com"), true);
  });

  it("should be valid namespace", () => {
    strictEqual(isValidNamespace("domain.com"), true);
    strictEqual(isValidNamespace("domain.com.cn"), true);
    strictEqual(isValidNamespace("domain.com.cn.cn"), true);
    strictEqual(isValidNamespace(".com.cn.cn.cn"), false);
    strictEqual(isValidNamespace("com.cn.cn."), false);
    strictEqual(isValidNamespace("sub.domain.com"), true);
    strictEqual(isValidNamespace("sub-domain.com"), true);
    strictEqual(isValidNamespace("sub.domain-test.com"), true);
    strictEqual(isValidNamespace("123.domain.com"), true);
    strictEqual(isValidNamespace("domain.123.com"), true);
    strictEqual(isValidNamespace("domain.com123"), true);
    strictEqual(isValidNamespace("domain.com-"), true);
    strictEqual(isValidNamespace("-domain.com"), true);
    strictEqual(isValidNamespace("domain.-com"), true);
    strictEqual(isValidNamespace("domain..com"), false);
    strictEqual(isValidNamespace(".domain"), false);
    strictEqual(isValidNamespace("domain.123"), true);
    strictEqual(isValidNamespace("domain.123.123"), true);
    strictEqual(isValidNamespace("a.b.c.d"), true);
    strictEqual(isValidNamespace("a.b.c.123"), true);
    strictEqual(isValidNamespace("a.b.c.d-"), true);
    strictEqual(isValidNamespace("a.b.c.-d"), true);
    strictEqual(isValidNamespace("a.b.c..d"), false);
    strictEqual(isValidNamespace("a.b.c."), false);
    strictEqual(isValidNamespace(".a.b.c"), false);
    strictEqual(isValidNamespace("."), false);
    strictEqual(isValidNamespace(".."), false);
    strictEqual(isValidNamespace("..."), false);
    strictEqual(isValidNamespace("....domain.com"), false);
    strictEqual(isValidNamespace("domain.com...."), false);
  });
});
