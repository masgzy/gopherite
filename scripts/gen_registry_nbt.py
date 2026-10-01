#!/usr/bin/env python3
"""Generate the network-NBT registry blob for Gopherite's configuration phase.

Reads the vanilla 26.2 server jar's bundled datapack JSON, converts each
synchronized-registry entry to network NBT (rootless compound, big endian),
and packs everything into a single binary blob consumed by Go via go:embed.

Blob layout (all integers big endian):
  u8      registry count
  per registry:
    u8    key length, key bytes (utf-8, e.g. "minecraft:worldgen/biome")
    u16   entry count
    per entry:
      u8    name length, name bytes (utf-8, without namespace)
      u32   nbt length, nbt bytes (rootless compound NBT)

Conversion mirrors vanilla's datapack-JSON -> Java value -> NbtOps path:
  object  -> TAG_Compound        string -> TAG_String
  array   -> TAG_List (element type unified; empty list writes type 0)
  integer -> TAG_Int (TAG_Long when out of int32 range)
  decimal -> TAG_Double          true/false -> TAG_Byte 1/0
Codecs read values through NumericTag, so int-vs-double widening is safe.
"""

import io
import json
import struct
import sys
import zipfile

JAR = "/home/z/my-project/research/extracted/META-INF/versions/26.2/server-26.2.jar"
OUT = "/home/z/my-project/gopherite/protocol/java/v776/registry_data.bin"

# Synchronized registries in vanilla SYNCHRONIZED_REGISTRIES order
# (net.minecraft.resources.RegistryDataLoader).
REGISTRIES = [
    "minecraft:worldgen/biome",
    "minecraft:chat_type",
    "minecraft:trim_pattern",
    "minecraft:trim_material",
    "minecraft:wolf_variant",
    "minecraft:wolf_sound_variant",
    "minecraft:pig_variant",
    "minecraft:pig_sound_variant",
    "minecraft:frog_variant",
    "minecraft:cat_variant",
    "minecraft:cat_sound_variant",
    "minecraft:cow_sound_variant",
    "minecraft:cow_variant",
    "minecraft:chicken_sound_variant",
    "minecraft:chicken_variant",
    "minecraft:zombie_nautilus_variant",
    "minecraft:painting_variant",
    "minecraft:sulfur_cube_archetype",
    "minecraft:dimension_type",
    "minecraft:damage_type",
    "minecraft:banner_pattern",
    "minecraft:enchantment",
    "minecraft:jukebox_song",
    "minecraft:instrument",
    "minecraft:test_environment",
    "minecraft:test_instance",
    "minecraft:dialog",
    "minecraft:world_clock",
    "minecraft:timeline",
]

TAG_BYTE, TAG_SHORT, TAG_INT, TAG_LONG = 1, 2, 3, 4
TAG_FLOAT, TAG_DOUBLE, TAG_BYTE_ARRAY = 5, 6, 7
TAG_STRING, TAG_LIST, TAG_COMPOUND = 8, 9, 10
TAG_INT_ARRAY, TAG_LONG_ARRAY = 11, 12
TAG_END = 0


def write_string(out: io.BytesIO, s: str):
    b = s.encode("utf-8")
    out.write(struct.pack(">H", len(b)))
    out.write(b)


class IntAware:
    __slots__ = ("value", "was_int")

    def __init__(self, value, was_int):
        self.value = value
        self.was_int = was_int


def make_hooks():
    seen = {}

    def parse_int(s):
        v = int(s)
        obj = IntAware(v, True)
        seen[id(obj)] = True
        return obj

    def parse_float(s):
        v = float(s)
        obj = IntAware(v, False)
        seen[id(obj)] = True
        return obj

    return parse_int, parse_float


def unwrap(v):
    if isinstance(v, IntAware):
        return v.value, v.was_int
    if isinstance(v, bool):
        return v, True
    if isinstance(v, int):
        return v, True
    if isinstance(v, float):
        return v, False
    raise TypeError(type(v))


def write_value(out: io.BytesIO, tag: int, v):
    """Write payload for a value that has already been tagged."""
    if tag == TAG_BYTE:
        out.write(struct.pack(">b", int(v)))
    elif tag == TAG_SHORT:
        out.write(struct.pack(">h", int(v)))
    elif tag == TAG_INT:
        out.write(struct.pack(">i", int(v)))
    elif tag == TAG_LONG:
        out.write(struct.pack(">q", int(v)))
    elif tag == TAG_FLOAT:
        out.write(struct.pack(">f", v))
    elif tag == TAG_DOUBLE:
        out.write(struct.pack(">d", v))
    elif tag == TAG_STRING:
        write_string(out, v)
    elif tag == TAG_LIST:
        items, etype = v
        out.write(struct.pack(">b", etype))
        out.write(struct.pack(">i", len(items)))
        for item in items:
            out.write(item)  # pre-serialised payloads
    elif tag == TAG_COMPOUND:
        for (k, et, ev) in v:
            out.write(struct.pack(">b", et))
            write_string(out, k)
            out.write(ev)
        out.write(struct.pack(">b", TAG_END))
    else:
        raise TypeError(f"unsupported tag {tag}")


def kind_of(v) -> str:
    if isinstance(v, dict):
        return "compound"
    if isinstance(v, list):
        return "list"
    if isinstance(v, str):
        return "string"
    if isinstance(v, bool):
        return "byte"
    value, was_int = unwrap(v)
    return "int" if was_int else "double"


def pack_list(etype: int, payloads: list) -> bytes:
    out = io.BytesIO()
    write_value(out, TAG_LIST, (payloads, etype))
    return out.getvalue()


def widen(v, forced: int) -> bytes:
    """Serialise one numeric (possibly nested-list) member under a forced
    numeric element type."""
    out = io.BytesIO()
    if isinstance(v, list):
        payloads = [widen(item, forced) for item in v]
        write_value(out, TAG_LIST, (payloads, forced))
        return out.getvalue()
    value, _ = unwrap(v)
    write_value(out, forced, value)
    return out.getvalue()


def list_repr(v) -> tuple[int, list]:
    """Return (element tag, member payloads) for a uniform NBT ListTag body
    covering the JSON list v. NBT lists require one element type, so
    numeric families are unified (int lists widen to double when any
    member is fractional; nested lists are handled recursively)."""
    if not v:
        return TAG_END, []
    kinds = {kind_of(item) for item in v}
    if kinds == {"compound"}:
        return TAG_COMPOUND, [serialise(item)[1] for item in v]
    if kinds == {"string"}:
        return TAG_STRING, [serialise(item)[1] for item in v]
    if kinds == {"list"}:
        # Nested lists: the outer ListTag's element type is TAG_LIST itself;
        # each inner list carries its own element type byte (an empty inner
        # list writes type 0), exactly like vanilla's NbtOps output.
        return TAG_LIST, [pack_list(etype, payloads)
                          for etype, payloads in
                          (list_repr(item) for item in v)]
    if kinds <= {"int", "double", "byte"}:
        if kinds == {"byte"}:
            return TAG_BYTE, [widen(item, TAG_BYTE) for item in v]
        if "double" in kinds or "byte" in kinds:
            return TAG_DOUBLE, [widen(item, TAG_DOUBLE) for item in v]
        if all(-2**31 <= unwrap(item)[0] < 2**31
               for item in v if not isinstance(item, bool)):
            return TAG_INT, [widen(item, TAG_INT) for item in v]
        return TAG_LONG, [widen(item, TAG_LONG) for item in v]
    raise TypeError(f"heterogeneous list kinds {kinds}")


def serialise(v) -> tuple[int, bytes]:
    """Return (tag, payload-bytes) for a JSON value."""
    out = io.BytesIO()
    if isinstance(v, dict):
        fields = []
        for k, val in v.items():
            t, payload = serialise(val)
            fields.append((k, t, payload))
        write_value(out, TAG_COMPOUND, fields)
        return TAG_COMPOUND, out.getvalue()
    if isinstance(v, list):
        etype, payloads = list_repr(v)
        write_value(out, TAG_LIST, (payloads, etype))
        return TAG_LIST, out.getvalue()
    if isinstance(v, str):
        write_value(out, TAG_STRING, v)
        return TAG_STRING, out.getvalue()
    if isinstance(v, bool):
        write_value(out, TAG_BYTE, 1 if v else 0)
        return TAG_BYTE, out.getvalue()
    value, was_int = unwrap(v)
    if was_int:
        tag = TAG_INT if -(2**31) <= value < 2**31 else TAG_LONG
        write_value(out, tag, value)
        return tag, out.getvalue()
    write_value(out, TAG_DOUBLE, value)
    return TAG_DOUBLE, out.getvalue()


def entry_nbt(doc) -> bytes:
    """Rootless network NBT compound for one registry entry: the leading
    0x0A root-type byte is kept, the root name string is omitted (the
    1.20.2+ network NBT format)."""
    tag, payload = serialise(doc)
    assert tag == TAG_COMPOUND, "registry entries must be objects"
    return b"\x0a" + payload


def main():
    parse_int, parse_float = make_hooks()
    zf = zipfile.ZipFile(JAR)
    names = set(zf.namelist())
    blob = io.BytesIO()
    blob.write(struct.pack(">B", len(REGISTRIES)))
    total_entries = 0
    for key in REGISTRIES:
        short = key.split(":", 1)[1]
        candidates = [f"data/minecraft/{short}/"]
        if "/" in short:
            base = short.split("/", 1)[1]
            candidates.append(f"data/minecraft/{base}/")
        files = None
        for cand in candidates:
            hits = sorted(n for n in names
                          if n.startswith(cand) and n.endswith(".json"))
            if hits:
                files = hits
                break
        if not files:
            print(f"ERROR: no data files for {key}", file=sys.stderr)
            sys.exit(1)
        kb = key.encode("utf-8")
        blob.write(struct.pack(">B", len(kb)))
        blob.write(kb)
        blob.write(struct.pack(">H", len(files)))
        for path in files:
            name = path[len(candidates[0] if path.startswith(candidates[0])
                            else candidates[1]):-5]
            doc = json.loads(zf.read(path), parse_int=parse_int,
                             parse_float=parse_float)
            nbt = entry_nbt(doc)
            nb = name.encode("utf-8")
            blob.write(struct.pack(">B", len(nb)))
            blob.write(nb)
            blob.write(struct.pack(">I", len(nbt)))
            blob.write(nbt)
            total_entries += 1
    data = blob.getvalue()
    with open(OUT, "wb") as f:
        f.write(data)
    print(f"wrote {OUT}: {len(data)} bytes, {total_entries} entries "
          f"across {len(REGISTRIES)} registries")


if __name__ == "__main__":
    main()
