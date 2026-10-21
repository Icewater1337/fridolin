package secretxfer

import (
	"crypto/sha256"
	"strings"
)

// wordList is 256 short, distinct words: each byte of the short
// authentication string is one of them.
var wordList = strings.Fields(`
acorn actor agent alarm album alley amber angle ankle apple apron arena
armor arrow aspen atlas attic autumn badge bagel baker bamboo banjo barn
basil basin beach beacon beetle bell bench berry bison blade blanket blaze
bloom board boat boot bottle boulder bow brass bread brick bridge brook
broom bucket bugle bunny butter cabin cactus camel candle canoe canyon
carbon carpet carrot castle cedar cello chalk cherry chess chimney cider
circus citrus clay cliff clock cloud clover coast cobalt cocoa comet copper
coral cotton cougar crane crayon cricket crown crystal cupcake curtain daisy
dancer delta desert diamond dingo dolphin donkey dragon drum eagle easel
echo eclipse elbow ember engine falcon feather fern ferry fiddle finch flame
flute forest fossil fox galaxy garden garlic gecko geyser ginger glacier
globe goose granite grape gravel guitar hammer harbor harp hazel helmet
heron honey hornet indigo iris island ivory jacket jaguar jasmine jelly
jewel jungle kayak kettle kiwi koala ladder lagoon lantern lemon lily linen
lizard lobster locket lotus magnet mango maple marble meadow melon mitten
monkey moose mosaic moth muffin nectar needle nickel nutmeg oasis ocean
olive onion orbit orchid otter owl oyster paddle panda parrot peach pebble
pepper piano pigeon pillow pine planet plum pocket pony poppy prism pumpkin
quail quartz quilt rabbit radar raven reef ribbon river robin rocket saddle
salmon satin scarf shadow shell silver sketch sled snail sparrow spider
spruce squid star stone sugar summit swan temple thistle thunder tiger
timber toast tomato topaz tulip tunnel turtle velvet violin walnut walrus
willow window wizard zebra
`)

// sasDomain separates the words' hash from any other use of the keys.
const sasDomain = "fridolin secrets words v1"

// Words is the short authentication string of a transfer: four words
// from the SHA-256 of both sides' public keys. Each side computes it from
// the keys it saw; a party in the middle that swapped a key makes the two
// sides show different words.
func Words(receiverPub, senderPub string) string {
	sum := sha256.Sum256([]byte(sasDomain + "|" + receiverPub + "|" + senderPub))
	w := make([]string, 4)
	for i := range w {
		w[i] = wordList[sum[i]]
	}
	return strings.Join(w, " ")
}
