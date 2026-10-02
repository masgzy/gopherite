#!/usr/bin/env python3
"""Generate server/recipes_gen.go from the vanilla recipe datapack.

Ground truth: data/minecraft/recipe/*.json and data/minecraft/tags/item/
*.json shipped inside the vanilla server jar (see
scripts/extract_vanilla_data.py). Run after a Minecraft version bump:

  python3 scripts/gen_recipes.py --data data/minecraft \
      --out server/recipes_gen.go

The generator resolves item tags recursively at generation time, so the
Go side only sees plain item identifiers. Ingredient alternatives are
joined with '|', tags keep their '#minecraft:x' spelling. Code-driven
"special" recipes are emitted with whatever fields their JSON carries;
their behavior lives in Go, not data.
"""
import argparse
import glob
import json
import os


def norm(name):
    """Add the minecraft namespace when missing."""
    if ":" in name:
        return name
    return "minecraft:" + name


class Tags:
    def __init__(self, tagdir):
        self.raw = {}
        pattern = os.path.join(glob.escape(tagdir), "**", "*.json")
        for p in sorted(glob.glob(pattern, recursive=True)):
            rel = os.path.relpath(p, tagdir)[:-5]  # strip .json
            tag = "minecraft:" + rel.replace(os.sep, "/")
            doc = json.load(open(p))
            self.raw[tag] = [norm(v) for v in doc.get("values", [])]
        self.missing = set()
        self.resolved = {}
        for tag in self.raw:
            self.items(tag)

    def items(self, tag, stack=()):
        if tag in self.resolved:
            return self.resolved[tag]
        if tag not in self.raw:
            # Cross-registry tags (e.g. 26.2 sulfur cube archetypes) are
            # registered in code, not as item-tag JSON; they match no
            # crafting ingredient and resolve to the empty set.
            self.missing.add(tag)
            self.resolved[tag] = set()
            return self.resolved[tag]
        if tag in stack:
            raise SystemExit(f"tag cycle: {' -> '.join(stack + (tag,))}")
        out = set()
        for v in self.raw[tag]:
            if v.startswith("#"):
                out |= self.items(v[1:], stack + (tag,))
            else:
                out.add(v)
        self.resolved[tag] = out
        return out


def join_ingr(value):
    """Encode one ingredient JSON value as our expression string."""
    if isinstance(value, str):
        return norm(value)
    if isinstance(value, list):
        return "|".join(norm(v) for v in value)
    raise SystemExit(f"unsupported ingredient value: {value!r}")


FIELD_ORDER = ["back", "front", "left", "right", "ingredient", "input",
               "material", "base", "addition", "template", "source", "dye",
               "target", "banner", "map", "fuel", "shell", "star"]


def is_ingredient(value):
    # Non-ingredient fields (booleans, ints, the smithing trim pattern
    # id, the firework star's shape -> ingredient map) are code-behavior
    # details, not data slots.
    return isinstance(value, (str, list))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--data", default="data/minecraft")
    ap.add_argument("--out", default="server/recipes_gen.go")
    args = ap.parse_args()

    tags = Tags(os.path.join(args.data, "tags", "item"))
    recipes = []
    for p in sorted(glob.glob(os.path.join(args.data, "recipe", "*.json"))):
        name = "minecraft:" + os.path.basename(p)[:-5]
        r = json.load(open(p))
        kind = norm(r["type"])
        rec = {"name": name, "kind": kind}
        if "group" in r:
            rec["group"] = r["group"]
        if "category" in r:
            rec["category"] = r["category"]

        if kind == "minecraft:crafting_shaped":
            rec["pattern"] = r["pattern"]
            rec["keys"] = {s: join_ingr(v) for s, v in r["key"].items()}
        elif kind == "minecraft:crafting_shapeless":
            rec["ingredients"] = [join_ingr(v) for v in r["ingredients"]]
        else:
            slots = []
            fields = [f for f in FIELD_ORDER if is_ingredient(r.get(f))]
            fields += sorted(f for f in (set(r) - set(FIELD_ORDER)
                                         - {"type", "group", "category",
                                            "result", "experience",
                                            "cookingtime", "pattern",
                                            "show_notification"})
                             if is_ingredient(r[f]))
            for f in fields:
                slots.append((f, join_ingr(r[f])))
            rec["slots"] = slots

        res = r.get("result")
        if isinstance(res, dict):
            rec["result"] = norm(res["id"])
            if res.get("count", 1) != 1:
                rec["count"] = res["count"]
            if "components" in res:
                rec["components"] = json.dumps(res["components"],
                                               separators=(",", ":"))
        elif isinstance(res, str):
            rec["result"] = norm(res)
        if "experience" in r:
            rec["experience"] = r["experience"]
        if "cookingtime" in r:
            rec["cookingtime"] = r["cookingtime"]
        recipes.append(rec)

    # Keep names sorted for stable output.
    recipes.sort(key=lambda r: r["name"])

    with open(args.out, "w") as f:
        f.write("// Code generated by scripts/gen_recipes.py from the vanilla 26.2\n")
        f.write("// recipe datapack (scripts/extract_vanilla_data.py); DO NOT EDIT.\n\n")
        f.write("package server\n\n")
        f.write("// itemTags lists the members of every vanilla item tag,\n")
        f.write("// resolved recursively at generation time.\n")
        f.write("var itemTags = map[string][]string{\n")
        for tag in sorted(tags.resolved):
            members = sorted(tags.resolved[tag])
            f.write(f'\t"{tag}": {{')
            f.write(", ".join(f'"{m}"' for m in members))
            f.write("},\n")
        f.write("}\n\n")
        f.write("// recipeDefs is the full vanilla recipe table (data-driven and\n")
        f.write('// "special" kinds alike), one entry per datapack recipe file.\n')
        f.write("var recipeDefs = []recipeDef{\n")
        for r in recipes:
            f.write("\t{\n")
            f.write(f'\t\tName: "{r["name"]}", Kind: "{r["kind"]}",\n')
            if "group" in r:
                f.write(f'\t\tGroup: "{r["group"]}",\n')
            if "category" in r:
                f.write(f'\t\tCategory: "{r["category"]}",\n')
            if "pattern" in r:
                pat = ", ".join(f'"{row}"' for row in r["pattern"])
                f.write(f"\t\tPattern: []string{{{pat}}},\n")
                keys = ", ".join(
                    f'{{Sym: \'{s}\', Ingr: "{r["keys"][s]}"}}'
                    for s in sorted(r["keys"]))
                f.write(f"\t\tKeys: []symBinding{{{keys}}},\n")
            if "ingredients" in r:
                ing = ", ".join(f'"{i}"' for i in r["ingredients"])
                f.write(f"\t\tIngredients: []string{{{ing}}},\n")
            if r.get("slots"):
                slots = ", ".join(
                    f'{{Role: "{role}", Ingr: "{ing}"}}'
                    for role, ing in r["slots"])
                f.write(f"\t\tSlots: []slot{{{slots}}},\n")
            if r.get("result"):
                f.write(f'\t\tResult: "{r["result"]}",\n')
            if r.get("count"):
                f.write(f'\t\tCount: {r["count"]},\n')
            if r.get("components"):
                f.write(f'\t\tComponents: `{r["components"]}`,\n')
            if "experience" in r:
                f.write(f'\t\tExp: {r["experience"]},\n')
            if "cookingtime" in r:
                f.write(f'\t\tCookTime: {r["cookingtime"]},\n')
            f.write("\t},\n")
        f.write("}\n")

    kinds = {}
    for r in recipes:
        kinds[r["kind"]] = kinds.get(r["kind"], 0) + 1
    print(f"{len(recipes)} recipes, {len(tags.resolved)} item tags -> {args.out}")
    for m in sorted(tags.missing):
        print(f"  warn: unresolved tag ref {m} (code-registered)")
    for k in sorted(kinds):
        print(f"  {k}: {kinds[k]}")


if __name__ == "__main__":
    main()
