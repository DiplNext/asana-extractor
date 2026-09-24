<!-- aif:research-mode:ultra -->
# Research Index: Asana Extractor exercise

Topic: Asana users/projects extractor CLI (take-home exercise)
Slug: asana-extractor-exercise
Updated: 2026-09-24 15:21
Status: active

## Purpose
Record verified Asana API behavior and the design decisions for a stdlib-only Go CLI that exports one workspace's users and projects to per-resource JSON files on a schedule.

## Artifact Index

| Artifact | Purpose | Why included | Status |
|----------|---------|--------------|--------|
| [RESEARCH.md](RESEARCH.md) | Active summary and session history | Required | active |

No C4, ADR or dependency artifacts: the whole flow runs in one binary, dependencies go one way in a straight line (cmd → extractor → asana/storage), and the decisions are cheap to reverse.

## Reading Order
1. [RESEARCH.md](RESEARCH.md)
