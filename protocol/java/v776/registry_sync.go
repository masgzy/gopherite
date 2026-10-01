package v776

// Registry entries for the 29 dynamically synchronized registries, in
// vanilla registration order (sorted datapack path order). Sent as
// Registry Data packets with empty payloads when the client reports
// knowledge of the vanilla core pack; the client then rebuilds entries
// from its built-in data using this exact ordering.
// Generated from the vanilla 26.2 datapack by scripts/gen_registries.py;
// regenerate rather than hand-editing.

type registryEntries struct {
	Key     string
	Entries []string
}

var SyncRegistries = []registryEntries{
	{Key: "minecraft:worldgen/biome", Entries: []string{
		"badlands", "bamboo_jungle", "basalt_deltas", "beach", "birch_forest", "cherry_grove", "cold_ocean",
		"crimson_forest", "dark_forest", "deep_cold_ocean", "deep_dark", "deep_frozen_ocean", "deep_lukewarm_ocean",
		"deep_ocean", "desert", "dripstone_caves", "end_barrens", "end_highlands", "end_midlands",
		"eroded_badlands", "flower_forest", "forest", "frozen_ocean", "frozen_peaks", "frozen_river",
		"grove", "ice_spikes", "jagged_peaks", "jungle", "lukewarm_ocean", "lush_caves", "mangrove_swamp",
		"meadow", "mushroom_fields", "nether_wastes", "ocean", "old_growth_birch_forest", "old_growth_pine_taiga",
		"old_growth_spruce_taiga", "pale_garden", "plains", "river", "savanna", "savanna_plateau",
		"small_end_islands", "snowy_beach", "snowy_plains", "snowy_slopes", "snowy_taiga", "soul_sand_valley",
		"sparse_jungle", "stony_peaks", "stony_shore", "sulfur_caves", "sunflower_plains", "swamp",
		"taiga", "the_end", "the_void", "warm_ocean", "warped_forest", "windswept_forest", "windswept_gravelly_hills",
		"windswept_hills", "windswept_savanna", "wooded_badlands",
	}},
	{Key: "minecraft:chat_type", Entries: []string{
		"chat", "emote_command", "msg_command_incoming", "msg_command_outgoing", "say_command", "team_msg_command_incoming",
		"team_msg_command_outgoing",
	}},
	{Key: "minecraft:trim_pattern", Entries: []string{
		"bolt", "coast", "dune", "eye", "flow", "host", "raiser", "rib", "sentry", "shaper", "silence",
		"snout", "spire", "tide", "vex", "ward", "wayfinder", "wild",
	}},
	{Key: "minecraft:trim_material", Entries: []string{
		"amethyst", "copper", "diamond", "emerald", "gold", "iron", "lapis", "netherite", "quartz",
		"redstone", "resin",
	}},
	{Key: "minecraft:wolf_variant", Entries: []string{"ashen", "black", "chestnut", "pale", "rusty", "snowy", "spotted", "striped", "woods"}},
	{Key: "minecraft:wolf_sound_variant", Entries: []string{"angry", "big", "classic", "cute", "grumpy", "puglin", "sad"}},
	{Key: "minecraft:pig_variant", Entries: []string{"cold", "temperate", "warm"}},
	{Key: "minecraft:pig_sound_variant", Entries: []string{"big", "classic", "mini"}},
	{Key: "minecraft:frog_variant", Entries: []string{"cold", "temperate", "warm"}},
	{Key: "minecraft:cat_variant", Entries: []string{
		"all_black", "black", "british_shorthair", "calico", "jellie", "persian", "ragdoll", "red",
		"siamese", "tabby", "white",
	}},
	{Key: "minecraft:cat_sound_variant", Entries: []string{"classic", "royal"}},
	{Key: "minecraft:cow_sound_variant", Entries: []string{"classic", "moody"}},
	{Key: "minecraft:cow_variant", Entries: []string{"cold", "temperate", "warm"}},
	{Key: "minecraft:chicken_sound_variant", Entries: []string{"classic", "picky"}},
	{Key: "minecraft:chicken_variant", Entries: []string{"cold", "temperate", "warm"}},
	{Key: "minecraft:zombie_nautilus_variant", Entries: []string{"temperate", "warm"}},
	{Key: "minecraft:painting_variant", Entries: []string{
		"alban", "aztec", "aztec2", "backyard", "baroque", "bomb", "bouquet", "burning_skull", "bust",
		"cavebird", "changing", "cotan", "courbet", "creebet", "dennis", "donkey_kong", "earth",
		"endboss", "fern", "fighters", "finding", "fire", "graham", "humble", "kebab", "lowmist",
		"match", "meditative", "orb", "owlemons", "passage", "pigscene", "plant", "pointer", "pond",
		"pool", "prairie_ride", "sea", "skeleton", "skull_and_roses", "stage", "sunflowers", "sunset",
		"tides", "unpacked", "void", "wanderer", "wasteland", "water", "wind", "wither",
	}},
	{Key: "minecraft:sulfur_cube_archetype", Entries: []string{
		"bouncy", "explosive", "fast_flat", "fast_sliding", "high_resistance", "hot", "light", "regular",
		"slow_bouncy", "slow_flat", "slow_sliding", "sticky",
	}},
	{Key: "minecraft:dimension_type", Entries: []string{"overworld", "overworld_caves", "the_end", "the_nether"}},
	{Key: "minecraft:damage_type", Entries: []string{
		"arrow", "bad_respawn_point", "cactus", "campfire", "cramming", "dragon_breath", "drown",
		"dry_out", "ender_pearl", "explosion", "fall", "falling_anvil", "falling_block", "falling_stalactite",
		"fireball", "fireworks", "fly_into_wall", "freeze", "generic", "generic_kill", "hot_floor",
		"in_fire", "in_wall", "indirect_magic", "lava", "lightning_bolt", "mace_smash", "magic",
		"mob_attack", "mob_attack_no_aggro", "mob_projectile", "on_fire", "out_of_world", "outside_border",
		"player_attack", "player_explosion", "sonic_boom", "spear", "spit", "stalagmite", "starve",
		"sting", "sulfur_cube_hot", "sweet_berry_bush", "thorns", "thrown", "trident", "unattributed_fireball",
		"wind_charge", "wither", "wither_skull",
	}},
	{Key: "minecraft:banner_pattern", Entries: []string{
		"base", "border", "bricks", "circle", "creeper", "cross", "curly_border", "diagonal_left",
		"diagonal_right", "diagonal_up_left", "diagonal_up_right", "flow", "flower", "globe", "gradient",
		"gradient_up", "guster", "half_horizontal", "half_horizontal_bottom", "half_vertical", "half_vertical_right",
		"mojang", "piglin", "rhombus", "skull", "small_stripes", "square_bottom_left", "square_bottom_right",
		"square_top_left", "square_top_right", "straight_cross", "stripe_bottom", "stripe_center",
		"stripe_downleft", "stripe_downright", "stripe_left", "stripe_middle", "stripe_right", "stripe_top",
		"triangle_bottom", "triangle_top", "triangles_bottom", "triangles_top",
	}},
	{Key: "minecraft:enchantment", Entries: []string{
		"aqua_affinity", "bane_of_arthropods", "binding_curse", "blast_protection", "breach", "channeling",
		"density", "depth_strider", "efficiency", "feather_falling", "fire_aspect", "fire_protection",
		"flame", "fortune", "frost_walker", "impaling", "infinity", "knockback", "looting", "loyalty",
		"luck_of_the_sea", "lunge", "lure", "mending", "multishot", "piercing", "power", "projectile_protection",
		"protection", "punch", "quick_charge", "respiration", "riptide", "sharpness", "silk_touch",
		"smite", "soul_speed", "sweeping_edge", "swift_sneak", "thorns", "unbreaking", "vanishing_curse",
		"wind_burst",
	}},
	{Key: "minecraft:jukebox_song", Entries: []string{
		"11", "13", "5", "blocks", "bounce", "cat", "chirp", "creator", "creator_music_box", "far",
		"lava_chicken", "mall", "mellohi", "otherside", "pigstep", "precipice", "relic", "stal",
		"strad", "tears", "wait", "ward",
	}},
	{Key: "minecraft:instrument", Entries: []string{
		"admire_goat_horn", "call_goat_horn", "dream_goat_horn", "feel_goat_horn", "ponder_goat_horn",
		"seek_goat_horn", "sing_goat_horn", "yearn_goat_horn",
	}},
	{Key: "minecraft:test_environment", Entries: []string{"default"}},
	{Key: "minecraft:test_instance", Entries: []string{"always_pass"}},
	{Key: "minecraft:dialog", Entries: []string{"custom_options", "quick_actions", "server_links"}},
	{Key: "minecraft:world_clock", Entries: []string{"overworld", "the_end"}},
	{Key: "minecraft:timeline", Entries: []string{"day", "early_game", "moon", "villager_schedule"}},
}
