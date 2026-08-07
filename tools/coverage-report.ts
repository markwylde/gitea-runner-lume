#!/usr/bin/env node

// Turns a `go test -coverprofile` file into a human-friendly Markdown report:
// an overall total, a per-package summary sorted worst-first, and collapsible
// per-file details for each package.
//
// Coverage is statement-weighted (covered statements / total statements),
// matching `go tool cover -func`'s total, instead of naively averaging
// per-function percentages.
//
// Usage: node ./tools/coverage-report.ts -i coverage.txt -o .tmp/coverage.md

import {readFileSync, writeFileSync} from 'node:fs';
import {basename, dirname} from 'node:path';
import {argv, exit, stderr} from 'node:process';

const modulePrefix = 'gitea.com/gitea/runner/';

type Counter = {
  covered: number;
  total: number;
};

function percent(c: Counter): number {
  if (c.total === 0) {
    return 0;
  }
  return (c.covered / c.total) * 100;
}

function addCounter(a: Counter, b: Counter): Counter {
  return {covered: a.covered + b.covered, total: a.total + b.total};
}

function parseArgs(): {input: string; output: string} {
  let input = 'coverage.txt';
  let output = '.tmp/coverage.md';
  for (let i = 2; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === '-i' && argv[i + 1]) {
      input = argv[++i];
    } else if (arg === '-o' && argv[i + 1]) {
      output = argv[++i];
    }
  }
  return {input, output};
}

function parseProfile(name: string): Map<string, Counter> {
  const files = new Map<string, Counter>();
  const content = readFileSync(name, 'utf8');
  for (const line of content.split('\n')) {
    if (line === '' || line.startsWith('mode:')) {
      continue;
    }
    // Format: path:start.col,end.col numStmt count
    const colon = line.lastIndexOf(':');
    const fields = line.trimEnd().split(/\s+/);
    if (colon < 0 || fields.length < 3) {
      continue;
    }
    const file = line.slice(0, colon).replace(modulePrefix, '');
    const stmts = Number.parseInt(fields.at(-2)!, 10);
    const count = Number.parseInt(fields.at(-1)!, 10);
    if (Number.isNaN(stmts) || Number.isNaN(count)) {
      continue;
    }
    const c = files.get(file) ?? {covered: 0, total: 0};
    c.total += stmts;
    if (count > 0) {
      c.covered += stmts;
    }
    files.set(file, c);
  }
  return files;
}

function render(files: Map<string, Counter>): string {
  const pkgCounts = new Map<string, Counter>();
  const pkgFiles = new Map<string, string[]>();
  let total: Counter = {covered: 0, total: 0};

  for (const [file, c] of files) {
    const pkg = dirname(file);
    pkgCounts.set(pkg, addCounter(pkgCounts.get(pkg) ?? {covered: 0, total: 0}, c));
    const names = pkgFiles.get(pkg) ?? [];
    names.push(file);
    pkgFiles.set(pkg, names);
    total = addCounter(total, c);
  }

  const pkgs = [...pkgCounts.keys()];
  pkgs.sort((a, b) => {
    const ci = pkgCounts.get(a)!;
    const cj = pkgCounts.get(b)!;
    const pi = percent(ci);
    const pj = percent(cj);
    if (pi !== pj) {
      return pi - pj;
    }
    if (ci.total !== cj.total) {
      return cj.total - ci.total;
    }
    return a.localeCompare(b);
  });

  const lines: string[] = [];
  lines.push('# Coverage\n');
  lines.push(
    `**Total: ${percent(total).toFixed(1)}%** &nbsp;·&nbsp; ${total.covered} / ${total.total} statements covered &nbsp;·&nbsp; ${pkgs.length} packages\n`,
  );

  lines.push('## Packages\n');
  lines.push('| Package | Coverage | Statements |');
  lines.push('|---------|---------:|-----------:|');
  for (const pkg of pkgs) {
    const c = pkgCounts.get(pkg)!;
    lines.push(`| ${pkg} | ${percent(c).toFixed(1)}% | ${c.covered} / ${c.total} |`);
  }
  lines.push('');

  lines.push('## Files\n');
  for (const pkg of pkgs) {
    const c = pkgCounts.get(pkg)!;
    const names = [...(pkgFiles.get(pkg) ?? [])];
    names.sort((a, b) => {
      const ci = files.get(a)!;
      const cj = files.get(b)!;
      const pi = percent(ci);
      const pj = percent(cj);
      if (pi !== pj) {
        return pi - pj;
      }
      return a.localeCompare(b);
    });
    lines.push(
      `<details><summary><strong>${pkg}</strong> — ${percent(c).toFixed(1)}% (${c.covered}/${c.total})</summary>\n`,
    );
    lines.push('| File | Coverage | Statements |');
    lines.push('|------|---------:|-----------:|');
    for (const file of names) {
      const fc = files.get(file)!;
      lines.push(`| ${basename(file)} | ${percent(fc).toFixed(1)}% | ${fc.covered} / ${fc.total} |`);
    }
    lines.push('\n</details>\n');
  }

  return lines.join('\n');
}

function main(): void {
  const {input, output} = parseArgs();
  try {
    const files = parseProfile(input);
    writeFileSync(output, render(files), {mode: 0o644});
  } catch (err) {
    stderr.write(`coverage-report: ${err}\n`);
    exit(1);
  }
}

main();
