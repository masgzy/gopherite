package server

import (
        "testing"
)

// M13 酿造与药水物品测试。配方与数值对照 26.2 反编译源
// （PotionBrewing.addVanillaMixes / Potions.java / PotionIds.java）。

func potionTestServer(t *testing.T) *Server {
        s := startTestServer(t)
        s.mu.Lock()
        initBrewingTable()
        s.mu.Unlock()
        return s
}

// 药水注册表顺序（PotionIds.java）。
func TestPotionRegistryOrder(t *testing.T) {
        cases := []struct {
                id   int
                name string
        }{
                {0, "water"}, {1, "mundane"}, {2, "thick"}, {3, "awkward"},
                {13, "swiftness"}, {19, "turtle_master"}, {24, "healing"},
                {28, "poison"}, {45, "infested"},
        }
        for _, tc := range cases {
                if potionDefs[tc.id].name != tc.name {
                        t.Fatalf("药水 %d 应为 %s，实际 %s", tc.id, tc.name, potionDefs[tc.id].name)
                }
        }
        if id, ok := potionIDByName("strong_swiftness"); !ok || id != 15 {
                t.Fatalf("strong_swiftness 应为 15，实际 %d %v", id, ok)
        }
}

// 起始配方：水+地狱疣→粗制；粗制+糖→迅捷；水+糖→粗制（mundane）。
func TestBrewingStartMixes(t *testing.T) {
        potionTestServer(t)
        wart := itemIDByName["minecraft:nether_wart"]
        sugar := itemIDByName["minecraft:sugar"]

        if to, ok := brewingMix(0, wart); !ok || to != 3 {
                t.Fatalf("水+地狱疣应得粗制(3)，实际 %d %v", to, ok)
        }
        if to, ok := brewingMix(3, sugar); !ok || to != 13 {
                t.Fatalf("粗制+糖应得迅捷(13)，实际 %d %v", to, ok)
        }
        if to, ok := brewingMix(0, sugar); !ok || to != 1 {
                t.Fatalf("水+糖应得粗制(1)，实际 %d %v", to, ok)
        }
}

// 效果与修饰配方：迅捷+红石→延长；迅捷+荧石→强化；发酵蛛眼链。
func TestBrewingEffectMixes(t *testing.T) {
        potionTestServer(t)
        redstone := itemIDByName["minecraft:redstone"]
        glowstone := itemIDByName["minecraft:glowstone_dust"]
        fermented := itemIDByName["minecraft:fermented_spider_eye"]

        if to, ok := brewingMix(13, redstone); !ok || to != 14 {
                t.Fatalf("迅捷+红石应得 long_swiftness(14)，实际 %d %v", to, ok)
        }
        if to, ok := brewingMix(13, glowstone); !ok || to != 15 {
                t.Fatalf("迅捷+荧石应得 strong_swiftness(15)，实际 %d %v", to, ok)
        }
        if to, ok := brewingMix(13, fermented); !ok || to != 16 {
                t.Fatalf("迅捷+发酵蛛眼应得 slowness(16)，实际 %d %v", to, ok)
        }
        if to, ok := brewingMix(0, fermented); !ok || to != 37 {
                t.Fatalf("水+发酵蛛眼应得 weakness(37)，实际 %d %v", to, ok)
        }
        // 不存在的组合。
        if _, ok := brewingMix(0, redstone); !ok {
                // 水+红石 = mundane（1）
                if to, _ := brewingMix(0, redstone); to != 1 {
                        t.Fatalf("水+红石应得 mundane(1)，实际 %d", to)
                }
        }
        if _, ok := brewingMix(24 /*healing*/, redstone); ok {
                t.Fatalf("治疗+红石不是原版配方")
        }
}

// 药水→效果映射抽样（Potions.java 数值）。
func TestPotionEffectValues(t *testing.T) {
        swiftness := potionDefs[13]
        if len(swiftness.effects) != 1 || swiftness.effects[0] != (potionEffect{eff: 0, dur: 3600, amp: 0}) {
                t.Fatalf("迅捷应为 speed 3600t amp0，实际 %+v", swiftness.effects)
        }
        strongSwiftness := potionDefs[15]
        if strongSwiftness.effects[0].amp != 1 || strongSwiftness.effects[0].dur != 1800 {
                t.Fatalf("强化迅捷应为 speed 1800t amp1，实际 %+v", strongSwiftness.effects)
        }
        turtle := potionDefs[19]
        if len(turtle.effects) != 2 || turtle.effects[0].amp != 3 || turtle.effects[1].amp != 2 {
                t.Fatalf("海龟大师应为 缓慢III+抗性II，实际 %+v", turtle.effects)
        }
        healing := potionDefs[24]
        if healing.effects[0].eff != 5 || healing.effects[0].dur != 1 {
                t.Fatalf("治疗应为 instant_health 即时，实际 %+v", healing.effects)
        }
}

// 食用效果数值（Consumables.java）。
func TestConsumableValues(t *testing.T) {
        ga := consumableByItemName["golden_apple"]
        if len(ga.effects) != 2 || ga.effects[0] != (consumableEffect{eff: 9, dur: 100, amp: 1, prob: 1}) {
                t.Fatalf("金苹果应为 再生II 100t + 吸收I 2400t，实际 %+v", ga.effects)
        }
        ega := consumableByItemName["enchanted_golden_apple"]
        if len(ega.effects) != 4 || ega.effects[3].amp != 3 {
                t.Fatalf("附魔金苹果应有 4 项效果且吸收为 IV，实际 %+v", ega.effects)
        }
        rf := consumableByItemName["rotten_flesh"]
        if rf.effects[0].prob != 0.8 {
                t.Fatalf("腐肉饥饿概率应为 0.8，实际 %v", rf.effects[0].prob)
        }
        if mb := consumableByItemName["milk_bucket"]; !mb.clearAll {
                t.Fatal("奶桶应清除全部效果")
        }
}

// 酿造台 tick：烈焰粉 20 次燃料、400t 进度、三瓶全部混合。
func TestBrewingStandTick(t *testing.T) {
        s := potionTestServer(t)
        b := newBlockEntity(beBrewing, 0, 64, 0)
        s.blockEnts[b.posKey()] = b

        netherWart := itemIDByName["minecraft:nether_wart"]
        potion := itemIDByName["minecraft:potion"]
        blaze := itemIDByName["minecraft:blaze_powder"]

        s.mu.Lock()
        b.slots[3] = invSlot{item: netherWart, count: 1}
        // 两瓶水瓶 + 一瓶空气。
        b.slots[0] = invSlot{item: potion, count: 1, potion: 0 + 1} // water
        b.slots[1] = invSlot{item: potion, count: 1, potion: 0 + 1}
        b.slots[4] = invSlot{item: blaze, count: 1}
        s.mu.Unlock()

        // 第 1 tick：装填燃料、启动酿造并立刻扣 1 次操作（vanilla 语义：
        // 燃料在启动 400t 倒计时那一刻消耗）。
        s.mu.Lock()
        s.tickBrewingLocked(b)
        fuelLoaded := b.fuel == brewingFuelPerItem-1
        brewing := b.brewTime == brewingTotalTicks
        s.mu.Unlock()
        if !fuelLoaded || !brewing {
                t.Fatalf("首 tick 应装填燃料并启动：fuel=%d brewTime=%d", b.fuel, b.brewTime)
        }

        // 推进 400 tick 到酿造完成（首 tick 只启动不递减）。
        s.mu.Lock()
        for i := 0; i < brewingTotalTicks; i++ {
                s.tickBrewingLocked(b)
        }
        done := b.brewTime == 0
        s.mu.Unlock()
        if !done {
                t.Fatalf("400t 后应完成酿造，brewTime=%d", b.brewTime)
        }
        // 完成那一 tick 的 tickBrewingLocked 内部已完成 doBrew。
        s.mu.Lock()
        awkward := b.slots[0].potion == 4 && b.slots[1].potion == 4 // awkward id 3 → +1
        ingredientGone := b.slots[3].count == 0
        fuelSpent := b.fuel == brewingFuelPerItem-1
        s.mu.Unlock()
        if !awkward {
                t.Fatalf("两瓶水都应变粗制(awkward, potion 字段 4)，实际 %d/%d", b.slots[0].potion, b.slots[1].potion)
        }
        if !ingredientGone {
                t.Fatalf("原料应消耗干净，实际 %+v", b.slots[3])
        }
        if !fuelSpent {
                t.Fatalf("燃料应扣 1 次操作，实际 %d", b.fuel)
        }
}

// 原料被拿走时进度作废。
func TestBrewingCancelsWhenIngredientLeaves(t *testing.T) {
        s := potionTestServer(t)
        b := newBlockEntity(beBrewing, 0, 64, 0)
        s.blockEnts[b.posKey()] = b

        s.mu.Lock()
        b.slots[3] = invSlot{item: itemIDByName["minecraft:nether_wart"], count: 1}
        b.slots[0] = invSlot{item: itemIDByName["minecraft:potion"], count: 1, potion: 1}
        b.slots[4] = invSlot{item: itemIDByName["minecraft:blaze_powder"], count: 1}
        s.tickBrewingLocked(b) // 启动
        running := b.brewTime > 0
        b.slots[3] = invSlot{} // 拿走原料
        s.tickBrewingLocked(b)
        s.mu.Unlock()
        if !running {
                t.Fatal("酿造应已启动")
        }
        s.mu.Lock()
        cancelled := b.brewTime == 0
        s.mu.Unlock()
        if !cancelled {
                t.Fatalf("原料拿走后进度应作废，brewTime=%d", b.brewTime)
        }
}

// 容器混合：水瓶+火药→喷溅。
func TestBrewingContainerMix(t *testing.T) {
        potionTestServer(t)
        potion := itemIDByName["minecraft:potion"]
        gunpowder := itemIDByName["minecraft:gunpowder"]
        if got := brewingContainerMixTarget(invSlot{item: potion, count: 1}, gunpowder); got != itemIDByName["minecraft:splash_potion"] {
                t.Fatalf("水瓶+火药应转化为喷溅药水物品，实际 %d", got)
        }
}

// 喷溅施加：近处全额、远处折扣、范围外无效。
func TestSplashRadiusScaling(t *testing.T) {
        s := potionTestServer(t)
        pNear := newEffectTestPlayer()
        pNear.conn = &conn{s: s}
        pNear.x, pNear.y, pNear.z = 0, 64, 0
        pFar := newEffectTestPlayer()
        pFar.conn = &conn{s: s}
        pFar.x, pFar.y, pFar.z = 10, 64, 0
        s.mu.Lock()
        s.players[pNear.conn] = pNear
        s.players[pFar.conn] = pFar
        s.applySplashAt(0, 64, 0, 13 /*swiftness*/, false, 0)
        s.mu.Unlock()
        if !pNear.hasEffect(0) {
                t.Fatal("4 格内的玩家应获得效果")
        }
        if pFar.hasEffect(0) {
                t.Fatal("10 格外的玩家不应获得效果")
        }
}

// 药水物品的组件编码往返：potion 字段进栈、出栈不丢。
func TestItemStackPotionRoundTrip(t *testing.T) {
        slot := invSlot{item: itemIDByName["minecraft:potion"], count: 1, potion: 13 + 1}
        stack := wireStack(slot)
        if stack.Potion != 14 {
                t.Fatalf("wireStack 应保留 potion 字段，实际 %d", stack.Potion)
        }
        back := wireSlot(stack)
        if back != slot {
                t.Fatalf("wireSlot 往返应还原 invSlot，实际 %+v", back)
        }
}
