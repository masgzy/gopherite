package server

// M17 command surface: /advancement grant|revoke <targets>
// (everything|from <adv>|through <adv>|until <adv>|only <adv> [<criterion>]).
// Wire behaviour mirrors vanilla AdvancementCommand (26.2): grant/revoke
// touch every criterion of the affected advancement set, everything
// spans the whole tree, from spans descendants, until spans ancestors
// and through spans both.

import (
	"fmt"
)

// registerM17Commands wires the /advancement root into the tree.
func registerM17Commands(root *cmdNode) {
	root.add(buildAdvancementCmd())
}

func buildAdvancementCmd() *cmdNode {
	adv := literalf("advancement")
	for _, mode := range []struct {
		literal string
		grant   bool
	}{{"grant", true}, {"revoke", false}} {
		modeNode := literalf(mode.literal)
		targets := argf("targets", "minecraft:entity", pEntity, "targets")
		// everything
		everything := literalf("everything").setExec(func(c *conn, args map[string]string) error {
			return runAdvancementCommand(c, args, mode.grant, func(t *advancementTree) []string {
				out := make([]string, 0, len(t.order))
				out = append(out, t.order...)
				return out
			})
		})
		targets.add(everything)

		// from / through / until share the single-advancement set shape.
		for _, span := range []struct {
			literal string
			kind    string // from / through / until
		}{{"from", "from"}, {"through", "through"}, {"until", "until"}} {
			spanNode := literalf(span.literal)
			advArg := argWord("advancement", "advancement")
			advArg.exec = true
			advArg.run = func(c *conn, args map[string]string) error {
				return runAdvancementSpan(c, args, mode.grant, span.kind, false)
			}
			// only <adv> [<criterion>] rides on the same argument node
			// shape but under its own literal.
			criterion := argWord("criterion", "criterion")
			criterion.exec = true
			criterion.run = func(c *conn, args map[string]string) error {
				return runAdvancementSpan(c, args, mode.grant, "only", true)
			}
			advArg.add(criterion)
			spanNode.add(advArg)
			targets.add(spanNode)
		}
		only := literalf("only")
		onlyAdv := argWord("advancement", "advancement")
		onlyAdv.exec = true
		onlyAdv.run = func(c *conn, args map[string]string) error {
			return runAdvancementSpan(c, args, mode.grant, "only", false)
		}
		onlyCrit := argWord("criterion", "criterion")
		onlyCrit.exec = true
		onlyCrit.run = func(c *conn, args map[string]string) error {
			return runAdvancementSpan(c, args, mode.grant, "only", true)
		}
		onlyAdv.add(onlyCrit)
		only.add(onlyAdv)
		targets.add(only)

		modeNode.add(targets)
		adv.add(modeNode)
	}
	return adv
}

// cmdRunError is the shared feedback/error sender for the command.
func advFeedback(c *conn, text string) error {
	return c.sendSystemChat(text)
}

// runAdvancementCommand executes grant/revoke everything with the given
// selection function.
func runAdvancementCommand(c *conn, args map[string]string, grant bool, selectIDs func(*advancementTree) []string) error {
	s := c.s
	targets := resolveTargets(s, c.player, args["targets"])
	if len(targets) == 0 {
		return advFeedback(c, "未找到目标玩家")
	}
	s.mu.Lock()
	count := 0
	for _, t := range targets {
		if t.adv == nil {
			continue
		}
		for _, id := range selectIDs(t.adv.tree) {
			count += s.advApplyToAllCriteria(t.adv, t, id, grant)
		}
	}
	s.mu.Unlock()
	verb := "撤销"
	if grant {
		verb = "授予"
	}
	return advFeedback(c, fmt.Sprintf("已%s %d 项进度判据", verb, count))
}

// runAdvancementSpan executes from/through/until/only.
func runAdvancementSpan(c *conn, args map[string]string, grant bool, kind string, withCriterion bool) error {
	s := c.s
	advID := args["advancement"]
	criterion := args["criterion"]
	targets := resolveTargets(s, c.player, args["targets"])
	if len(targets) == 0 {
		return advFeedback(c, "未找到目标玩家")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tree := targets[0].adv.tree
	if tree.defs[advID] == nil {
		return advFeedback(c, "未知进度: "+advID)
	}
	var ids []string
	switch kind {
	case "from":
		ids = tree.descendants(advID)
	case "through":
		ids = append(tree.ancestors(advID), tree.descendants(advID)...)
	case "until":
		ids = tree.ancestors(advID)
	case "only":
		ids = []string{advID}
	}
	count := 0
	for _, t := range targets {
		if t.adv == nil {
			continue
		}
		for _, id := range ids {
			if withCriterion {
				if criterion == "" {
					continue
				}
				if grant {
					if s.advAward(t.adv, t, id, criterion) {
						count++
					}
				} else if s.advRevoke(t.adv, t, id, criterion) {
					count++
				}
			} else {
				count += s.advApplyToAllCriteria(t.adv, t, id, grant)
			}
		}
	}
	verb := "撤销"
	if grant {
		verb = "授予"
	}
	return advFeedback(c, fmt.Sprintf("已%s %d 项进度判据", verb, count))
}

// advApplyToAllCriteria grants/revokes every criterion of one
// advancement (vanilla applyAdvancement: all names of the criteria map).
// Caller holds Server.mu.
func (s *Server) advApplyToAllCriteria(pa *playerAdvancements, p *player, id string, grant bool) int {
	def := pa.tree.defs[id]
	if def == nil {
		return 0
	}
	count := 0
	for _, c := range def.criteria {
		if grant {
			if s.advAward(pa, p, id, c.name) {
				count++
			}
		} else if s.advRevoke(pa, p, id, c.name) {
			count++
		}
	}
	return count
}
