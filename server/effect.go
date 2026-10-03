package server

import (
	"math"
	"strings"

	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// bareItemName strips the namespace prefix for mix-table matching
// (the generated registry uses full "minecraft:x" keys).
func bareItemName(id int32) string {
	return strings.TrimPrefix(itemNameOf(id), "minecraft:")
}

// M13 状态效果系统。全部数值与行为对照 26.2 反编译源核验
// （research/decomp_m13：MobEffects.java / MobEffectInstance.java、
// RegenerationMobEffect / PoisonMobEffect / WitherMobEffect /
// HungerMobEffect / HealOrHarmMobEffect 等）。

// effectDef 是一个状态效果的静态定义。ID 即 MobEffect 注册表序号
// （26.2 MobEffects.java 注册顺序，0 起）。
type effectDef struct {
	name    string
	id      int32
	instant bool // 即时效果：施加时立即结算一次，不长期驻留
}

// effectDefs 覆盖 26.2 全部 40 个效果。大部分效果对客户端是纯视觉
// （夜视、失明、反胃等由客户端自行渲染）；服务端只实现有可观测
// 行为的子集：速度/缓慢（生物移动）、急迫/挖掘疲劳（挖掘）、力量/
// 虚弱（近战）、抗性（减伤）、火焰免疫、失坠类（摔落伤害）、再生/
// 中毒/凋零/饥饿（周期结算）、瞬间治疗/伤害/饱和、吸收、隐身/发光
// （共享标志同步）。
var effectDefs = []effectDef{
	{"speed", 0, false},
	{"slowness", 1, false},
	{"haste", 2, false},
	{"mining_fatigue", 3, false},
	{"strength", 4, false},
	{"instant_health", 5, true},
	{"instant_damage", 6, true},
	{"jump_boost", 7, false},
	{"nausea", 8, false},
	{"regeneration", 9, false},
	{"resistance", 10, false},
	{"fire_resistance", 11, false},
	{"water_breathing", 12, false},
	{"invisibility", 13, false},
	{"blindness", 14, false},
	{"night_vision", 15, false},
	{"hunger", 16, false},
	{"weakness", 17, false},
	{"poison", 18, false},
	{"wither", 19, false},
	{"health_boost", 20, false},
	{"absorption", 21, false},
	{"saturation", 22, true},
	{"glowing", 23, false},
	{"levitation", 24, false},
	{"luck", 25, false},
	{"unluck", 26, false},
	{"slow_falling", 27, false},
	{"conduit_power", 28, false},
	{"dolphins_grace", 29, false},
	{"bad_omen", 30, false},
	{"hero_of_the_village", 31, false},
	{"darkness", 32, false},
	{"trial_omen", 33, false},
	{"raid_omen", 34, false},
	{"wind_charged", 35, false},
	{"weaving", 36, false},
	{"oozing", 37, false},
	{"infested", 38, false},
	{"breath_of_the_nautilus", 39, false},
}

// effectIDByName resolves "minecraft:speed" / "speed" style names.
func effectIDByName(name string) (int32, bool) {
	for i := range effectDefs {
		if effectDefs[i].name == name {
			return effectDefs[i].id, true
		}
	}
	return -1, false
}

// mobEffectInstance mirrors vanilla MobEffectInstance.
type mobEffectInstance struct {
	amp       int32 // 0-based amplifier
	duration  int32 // remaining ticks; counts down each tick
	ambient   bool
	particles bool
	icon      bool
}

// wireFlags renders the 26.2 Update Mob Effect flag byte.
func (e *mobEffectInstance) wireFlags() byte {
	var f byte
	if e.ambient {
		f |= java.EffectFlagAmbient
	}
	if e.particles {
		f |= java.EffectFlagParticles
	}
	if e.icon {
		f |= java.EffectFlagIcon
	}
	return f
}

// effectTarget 是可持有状态的实体（玩家或生物）。效果存储由各实体的
// 既有锁保护（玩家 s.mu、生物 m.mu）。
type effectTarget struct {
	effects map[int32]*mobEffectInstance
}

func (t *effectTarget) initEffects() {
	if t.effects == nil {
		t.effects = make(map[int32]*mobEffectInstance)
	}
}

func (t *effectTarget) getEffect(id int32) *mobEffectInstance {
	return t.effects[id]
}

func (t *effectTarget) removeEffectSlot(id int32) {
	delete(t.effects, id)
}

// hasEffect reports whether the entity currently holds the effect.
func (t *effectTarget) hasEffect(id int32) bool {
	return t.effects[id] != nil
}

// amplifierOf returns the current amplifier (or -1 when absent).
func (t *effectTarget) amplifierOf(id int32) int32 {
	if e := t.effects[id]; e != nil {
		return e.amp
	}
	return -1
}

// --- 周期结算间隔（decompiled shouldApplyEffectTickThisTick）--------------

// effectTickInterval returns the tick interval for the periodic effects
// (0 means every tick, like vanilla's interval <= 0 path).
func effectTickInterval(id int32, amp int32) int32 {
	switch id {
	case 9: // regeneration: 50 >> amplifier
		return 50 >> amp
	case 18: // poison: 25 >> amplifier
		return 25 >> amp
	case 19: // wither: 40 >> amplifier
		return 40 >> amp
	}
	return 0
}

// --- 效果颜色（MobEffects.java 26.2 构造参数，用于药水粒子色）--------------

// effectColors 按效果注册序给出 26.2 MobEffect 的渲染颜色（ARGB）。
// 来源：research/dec262/out5/MobEffects.java 逐条核验。
var effectColors = [40]int32{
	3402751,  // 0 speed
	9154528,  // 1 slowness
	14270531, // 2 haste
	4866583,  // 3 mining_fatigue
	16762624, // 4 strength
	16262179, // 5 instant_health
	11101546, // 6 instant_damage
	16646020, // 7 jump_boost
	5578058,  // 8 nausea
	13458603, // 9 regeneration
	9520880,  // 10 resistance
	16750848, // 11 fire_resistance
	10017472, // 12 water_breathing
	16185078, // 13 invisibility
	2039587,  // 14 blindness
	12779366, // 15 night_vision
	5797459,  // 16 hunger
	4738376,  // 17 weakness
	8889187,  // 18 poison
	7561558,  // 19 wither
	16284963, // 20 health_boost
	2445989,  // 21 absorption
	16262179, // 22 saturation
	9740385,  // 23 glowing
	13565951, // 24 levitation
	5882118,  // 25 luck
	12624973, // 26 unluck
	15978425, // 27 slow_falling
	1950417,  // 28 conduit_power
	8954814,  // 29 dolphins_grace
	745784,   // 30 bad_omen
	4521796,  // 31 hero_of_the_village
	2696993,  // 32 darkness
	1484454,  // 33 trial_omen
	14565464, // 34 raid_omen
	12438015, // 35 wind_charged
	7891290,  // 36 weaving
	10092451, // 37 oozing
	9214860,  // 38 infested
	65518,    // 39 breath_of_the_nautilus
}

// potionColor 复刻 PotionContents.getColor（26.2）：把每个可见效果的
// 颜色按 (amplifier+1) 加权平均；无效果时回退到 PotionDefaultColor
// （vanilla 的缺省 -13083194）。返回值已按 ARGB 不透明处理。
func potionColor(potionID int32) int32 {
	if potionID < 0 || int(potionID) >= len(potionDefs) {
		return v776.PotionDefaultColor
	}
	var r, g, b, weight int32
	for _, pe := range potionDefs[potionID].effects {
		c := effectColors[pe.eff]
		w := pe.amp + 1
		r += w * ((c >> 16) & 0xFF)
		g += w * ((c >> 8) & 0xFF)
		b += w * (c & 0xFF)
		weight += w
	}
	if weight == 0 {
		return v776.PotionDefaultColor
	}
	// 0xFF000000 超出 int32 正数域，直接用其补码表示（不透明 alpha）。
	return -16777216 | (r/weight)<<16 | (g/weight)<<8 | b/weight
}

// --- 药水注册表（PotionIds.java / Potions.java 26.2 权威值）---------------

// potionEffect is one effect granted by a potion.
type potionEffect struct {
	eff int32 // effect registry id
	dur int32 // duration ticks
	amp int32 // 0-based amplifier
}

// potionDef is one entry of the 26.2 potion registry; index = registry id.
type potionDef struct {
	name    string
	effects []potionEffect
}

var potionDefs = []potionDef{
	{"water", nil},
	{"mundane", nil},
	{"thick", nil},
	{"awkward", nil},
	{"night_vision", []potionEffect{{15, 3600, 0}}},
	{"long_night_vision", []potionEffect{{15, 9600, 0}}},
	{"invisibility", []potionEffect{{13, 3600, 0}}},
	{"long_invisibility", []potionEffect{{13, 9600, 0}}},
	{"leaping", []potionEffect{{7, 3600, 0}}},
	{"long_leaping", []potionEffect{{7, 9600, 0}}},
	{"strong_leaping", []potionEffect{{7, 1800, 1}}},
	{"fire_resistance", []potionEffect{{11, 3600, 0}}},
	{"long_fire_resistance", []potionEffect{{11, 9600, 0}}},
	{"swiftness", []potionEffect{{0, 3600, 0}}},
	{"long_swiftness", []potionEffect{{0, 9600, 0}}},
	{"strong_swiftness", []potionEffect{{0, 1800, 1}}},
	{"slowness", []potionEffect{{1, 1800, 0}}},
	{"long_slowness", []potionEffect{{1, 4800, 0}}},
	{"strong_slowness", []potionEffect{{1, 400, 3}}},
	{"turtle_master", []potionEffect{{1, 400, 3}, {10, 400, 2}}},
	{"long_turtle_master", []potionEffect{{1, 800, 3}, {10, 800, 2}}},
	{"strong_turtle_master", []potionEffect{{1, 400, 5}, {10, 400, 3}}},
	{"water_breathing", []potionEffect{{12, 3600, 0}}},
	{"long_water_breathing", []potionEffect{{12, 9600, 0}}},
	{"healing", []potionEffect{{5, 1, 0}}},
	{"strong_healing", []potionEffect{{5, 1, 1}}},
	{"harming", []potionEffect{{6, 1, 0}}},
	{"strong_harming", []potionEffect{{6, 1, 1}}},
	{"poison", []potionEffect{{18, 900, 0}}},
	{"long_poison", []potionEffect{{18, 1800, 0}}},
	{"strong_poison", []potionEffect{{18, 432, 1}}},
	{"regeneration", []potionEffect{{9, 900, 0}}},
	{"long_regeneration", []potionEffect{{9, 1800, 0}}},
	{"strong_regeneration", []potionEffect{{9, 450, 1}}},
	{"strength", []potionEffect{{4, 3600, 0}}},
	{"long_strength", []potionEffect{{4, 9600, 0}}},
	{"strong_strength", []potionEffect{{4, 1800, 1}}},
	{"weakness", []potionEffect{{17, 1800, 0}}},
	{"long_weakness", []potionEffect{{17, 4800, 0}}},
	{"luck", []potionEffect{{25, 6000, 0}}},
	{"slow_falling", []potionEffect{{27, 1800, 0}}},
	{"long_slow_falling", []potionEffect{{27, 4800, 0}}},
	{"wind_charged", []potionEffect{{35, 3600, 0}}},
	{"weaving", []potionEffect{{36, 3600, 0}}},
	{"oozing", []potionEffect{{37, 3600, 0}}},
	{"infested", []potionEffect{{38, 3600, 0}}},
}

// potionIDByName resolves a potion name to its registry id.
func potionIDByName(name string) (int32, bool) {
	for i := range potionDefs {
		if potionDefs[i].name == name {
			return int32(i), true
		}
	}
	return -1, false
}

// --- 酿造配方（PotionBrewing.addVanillaMixes 26.2 移植）--------------------
//
// addStartMix(ingredient, potion) 展开为两条：water+原料→mundane、
// awkward+原料→效果药水（与原版一致）。

type potionMix struct {
	from       int32 // potion registry id
	ingredient string
	to         int32
}

// 容器混合（gunpowder → 喷溅、dragon's breath → 滞留）在酿造结算时按
// 物品 ID 直接判断，不需要独立表。

// brewingMixTable resolves the decompiled mix rows into potion-id keyed
// entries; the ingredient stays a bare item name matched through the
// existing itemNameOf reverse table.
var brewingMixTable []potionMix

// initBrewingTable expands the vanilla addStartMix rows (every start
// ingredient needs BOTH water→mundane and awkward→potion). Called once
// from the server bootstrap.
func initBrewingTable() {
	// Start ingredients: (ingredient, awkward-mix result potion).
	startMixes := []struct {
		ingredient string
		to         int32
	}{
		{"golden_carrot", 4},           // night_vision
		{"magma_cream", 11},            // fire_resistance
		{"rabbit_foot", 8},             // leaping
		{"sugar", 13},                  // swiftness
		{"pufferfish", 22},             // water_breathing
		{"glistering_melon_slice", 24}, // healing
		{"spider_eye", 28},             // poison
		{"ghast_tear", 31},             // regeneration
		{"blaze_powder", 34},           // strength
		{"phantom_membrane", 40},       // slow_falling
		{"breeze_rod", 42},             // wind_charged
		{"cobweb", 43},                 // weaving
		{"slime_block", 44},            // oozing
		{"stone", 45},                  // infested
	}

	// Direct potion mixes (addMix rows, verbatim from the decompile).
	potionMixes := []potionMix{
		{0, "glowstone_dust", 2},        // water + glowstone → thick
		{0, "redstone", 1},              // water + redstone → mundane
		{0, "nether_wart", 3},           // water + nether wart → awkward
		{0, "fermented_spider_eye", 37}, // water + fermented → weakness
		{4, "redstone", 5},
		{4, "fermented_spider_eye", 6},
		{5, "fermented_spider_eye", 7},
		{6, "redstone", 7},
		{11, "redstone", 12},
		{8, "redstone", 9},
		{8, "glowstone_dust", 10},
		{8, "fermented_spider_eye", 16},
		{9, "fermented_spider_eye", 17},
		{16, "redstone", 17},
		{16, "glowstone_dust", 18},
		{19, "redstone", 20},
		{19, "glowstone_dust", 21},
		{13, "fermented_spider_eye", 16},
		{14, "fermented_spider_eye", 17},
		{13, "redstone", 14},
		{13, "glowstone_dust", 15},
		{22, "redstone", 23},
		{24, "glowstone_dust", 25},
		{24, "fermented_spider_eye", 26},
		{25, "fermented_spider_eye", 27},
		{26, "glowstone_dust", 27},
		{28, "fermented_spider_eye", 26},
		{29, "fermented_spider_eye", 26},
		{30, "fermented_spider_eye", 27},
		{28, "redstone", 29},
		{28, "glowstone_dust", 30},
		{31, "redstone", 32},
		{31, "glowstone_dust", 33},
		{34, "redstone", 35},
		{34, "glowstone_dust", 36},
		{37, "redstone", 38},
		{40, "redstone", 41},
	}

	brewingMixTable = make([]potionMix, 0, len(startMixes)*2+len(potionMixes))
	for _, sm := range startMixes {
		brewingMixTable = append(brewingMixTable,
			potionMix{0, sm.ingredient, 1}, // water + start ingredient → mundane
			potionMix{3, sm.ingredient, sm.to},
		)
	}
	brewingMixTable = append(brewingMixTable, potionMixes...)
}

// brewingHasMix reports whether (potion, ingredient) brews anything.
func brewingHasMix(potionID int32, ingredientItem int32) bool {
	if ingredientName := bareItemName(ingredientItem); ingredientName != "" {
		for _, m := range brewingMixTable {
			if m.from == potionID && m.ingredient == ingredientName {
				return true
			}
		}
	}
	return false
}

// brewingMix returns the result potion id for (potion, ingredient).
func brewingMix(potionID int32, ingredientItem int32) (int32, bool) {
	if ingredientName := bareItemName(ingredientItem); ingredientName != "" {
		for _, m := range brewingMixTable {
			if m.from == potionID && m.ingredient == ingredientName {
				return m.to, true
			}
		}
	}
	return -1, false
}

// brewingIsIngredient mirrors PotionBrewing.isIngredient: the item can
// take part in any mix (potion or container).
func brewingIsIngredient(item int32) bool {
	if name := bareItemName(item); name != "" {
		for _, m := range brewingMixTable {
			if m.ingredient == name {
				return true
			}
		}
	}
	return false
}

// --- 食用附加效果（Consumables.java 26.2 权威值）---------------------------

type consumableEffect struct {
	eff  int32
	dur  int32
	amp  int32
	prob float32 // 1.0 always applies; rotten flesh hunger is 0.8
}

// clearAllEffects marks milk buckets.
type foodConsumable struct {
	effects  []consumableEffect
	clearAll bool
}

var consumableByItemName = map[string]foodConsumable{
	"golden_apple": {effects: []consumableEffect{{9, 100, 1, 1}, {21, 2400, 0, 1}}},
	"enchanted_golden_apple": {effects: []consumableEffect{
		{9, 400, 1, 1}, {10, 6000, 0, 1}, {11, 6000, 0, 1}, {21, 2400, 3, 1}}},
	"rotten_flesh": {effects: []consumableEffect{{16, 600, 0, 0.8}}},
	"spider_eye":   {effects: []consumableEffect{{18, 100, 0, 1}}},
	"pufferfish":   {effects: []consumableEffect{{18, 1200, 1, 1}, {16, 300, 2, 1}, {8, 300, 0, 1}}},
	"milk_bucket":  {clearAll: true},
}

// --- 效果对属性/战斗的修正 --------------------------------------------------

// meleeBonusFromEffects computes the ADD_VALUE attack damage modifiers
// from strength (+3/level) and weakness (-4/level). Caller holds the
// entity's lock.
func meleeBonusFromEffects(t *effectTarget) float32 {
	bonus := float32(0)
	if st := t.getEffect(4); st != nil { // strength
		bonus += 3 * float32(st.amp+1)
	}
	if we := t.getEffect(17); we != nil { // weakness
		bonus -= 4 * float32(we.amp+1)
	}
	return bonus
}

// resistanceFactor returns the damage multiplier from the resistance
// effect (20% per level; vanilla caps at level 4+ → 80% total). Starve
// and out-of-world bypass it like vanilla.
func resistanceFactor(t *effectTarget, dmgType int32) float32 {
	if dmgType == v776.DamageTypeOutOfWorld || dmgType == v776.DamageTypeStarve {
		return 1
	}
	if r := t.getEffect(10); r != nil {
		return float32(math.Max(0.2, 1-0.2*float64(r.amp+1)))
	}
	return 1
}

// fireResistant reports immunity to fire/lava damage.
func fireResistant(t *effectTarget) bool {
	return t.hasEffect(11)
}

// miningSpeedFactor applies haste (+20%/level) and mining fatigue
// (×0.3 per level) to the dig speed, mirroring vanilla getDigSpeed.
func miningSpeedFactor(t *effectTarget) float64 {
	f := 1.0
	if h := t.getEffect(2); h != nil {
		f *= 1 + 0.2*float64(h.amp+1)
	}
	if mf := t.getEffect(3); mf != nil {
		f *= math.Pow(0.3, float64(mf.amp+1))
	}
	return f
}

// mobSpeedFactor applies speed (+20%/level) and slowness (-15%/level)
// to mob movement, mirroring the ADD_MULTIPLIED_TOTAL movement-speed
// modifiers registered by MobEffects.
func mobSpeedFactor(t *effectTarget) float64 {
	f := 1.0
	if s := t.getEffect(0); s != nil {
		f += 0.2 * float64(s.amp+1)
	}
	if sl := t.getEffect(1); sl != nil {
		f -= 0.15 * float64(sl.amp+1)
	}
	return math.Max(0, f)
}

// fallSafeDistanceBonus adds +1 per jump_boost level (safe fall distance
// attribute) and slow_falling nullifies fall damage entirely.
func fallDamageOverride(t *effectTarget) (noDamage bool, safeBonus float64) {
	if t.hasEffect(27) { // slow_falling
		return true, 0
	}
	if j := t.getEffect(7); j != nil { // jump_boost
		return false, float64(j.amp + 1)
	}
	return false, 0
}
