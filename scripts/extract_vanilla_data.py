#!/usr/bin/env python3
"""Extract the vanilla data inputs the generators need from a server jar.

  python3 scripts/extract_vanilla_data.py --jar server.jar --out vanilla/

Produces:
  vanilla/data/minecraft/recipe/*.json      -> scripts/gen_recipes.py
  vanilla/data/minecraft/tags/item/...      -> scripts/gen_recipes.py
  vanilla/reports/reports/*.json            -> gen_blockstates.py / gen_items.py
                                               (needs java, see below)

The reports come from the vanilla data generator; the script runs it
automatically when a JDK is on PATH:

  java -DbundlerMainClass=net.minecraft.data.Main -jar server.jar --reports

After extracting, regenerate the Go tables:

  make gen VANILLA=vanilla
"""
import argparse
import glob
import io
import os
import shutil
import subprocess
import sys
import zipfile


def extract_inner_zip(server_jar):
    """The 26.2 server jar is a bundler; the payload is the inner jar."""
    z = zipfile.ZipFile(server_jar)
    for n in z.namelist():
        m = n.startswith("META-INF/versions/") and n.endswith(".jar")
        if not m:
            continue
        inner = zipfile.ZipFile(io.BytesIO(z.read(n)))
        if any(x.startswith("data/minecraft/") for x in inner.namelist()):
            return inner
    raise SystemExit("no inner server jar with data/ found in bundler")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--jar", required=True)
    ap.add_argument("--out", default="vanilla")
    args = ap.parse_args()

    data_out = os.path.join(args.out, "data", "minecraft")
    inner = extract_inner_zip(args.jar)

    n = 0
    for name in inner.namelist():
        if name.startswith("data/minecraft/recipe/") and name.endswith(".json"):
            inner.extract(name, args.out)
            n += 1
        elif name.startswith("data/minecraft/tags/item/") and name.endswith(".json"):
            inner.extract(name, args.out)
    print(f"extracted {n} recipes + item tags -> {data_out}")

    reports = os.path.join(args.out, "reports")
    if os.path.isdir(os.path.join(reports, "reports")):
        print(f"reports already present: {reports}")
        return
    if shutil.which("java") is None:
        print("java not found; generate reports manually:")
        print(f"  java -DbundlerMainClass=net.minecraft.data.Main "
              f"-jar {args.jar} --reports")
        print(f"then copy reports/reports -> {reports}/reports")
        return
    print("running vanilla data generator (may take a minute) ...")
    subprocess.run(["java", "-DbundlerMainClass=net.minecraft.data.Main",
                    "-jar", os.path.abspath(args.jar), "--reports",
                    "--output", os.path.abspath(reports)], check=True)
    got = glob.glob(os.path.join(reports, "reports", "*.json"))
    print(f"reports: {len(got)} json files -> {reports}/reports")


if __name__ == "__main__":
    sys.exit(main())
