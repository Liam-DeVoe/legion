package specgraph

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"
	"testing"
	"testing/fstest"
)

// sweepFloors is, for each sweep and each base (sweepBases), the count of inputs that sweepAgainst
// judged when the table was written, by what the reader did with each: read it, as the base or as
// another state, or refused it, and the digest of the bytes that the reader read of the base and of
// each judged input, with each input's kind, its right and the places that the right allows, in the
// order of the inputs (sweepFloor, sweepCount.judged). The sweeps are the tree sweep
// (TestNoInputThatSaysNothingMovesAStateTowardTrue), the members added
// (TestAddingAMemberMovesNoStateTowardTrue), the texts inside a block and the attributes left out
// (TestNoNameOfNothingInsideABlockMovesAStateTowardTrue), and the refusals added
// (TestAddingARefusalOfCIMovesAStateTowardTrueOnlyByTakingOutItsCommitsResults). A sweep that judges
// fewer inputs of a base of either kind fails by name (atFloor), and so does a sweep whose inputs of
// a base hold, in their order, other bytes, read as other kinds or are judged by other rights than
// the digest's, and a base with no floor or a floor of no base (floorsCover). So a fault that drops
// inputs, of a sweep's meaning or of its writers, a sweep that builds inputs and does not derive
// them or drops an input from its judgment, a fault that moves inputs from one kind to the other,
// as many each way as it likes, and a change of the form of an input that keeps its kind (two
// spaces for one) cannot pass. The floors count what the reader did with an input, not what the
// judgment found: a judgment that stops reporting an input that moves a state toward true, and
// counts it as read, meets them. The judgment is tested over its whole domain
// (TestTheSweepsJudgmentAgreesWithItsDefinitionOverItsWholeDomain), and a sweep that does not call
// it fails by its known positives (knownPositives), one with no right and one with a right, at
// silent-leg and wait-cycle, which each sweep must report with the lines that each is built to move,
// and which no floor counts. A base, a scenario, a sweep or a reader that adds, takes out, moves or
// writes otherwise an input, or judges it by another right, changes the digest, so the table is
// written again, on purpose, from a run of the sweeps.
// It is a function that gives a new table at each call.
func sweepFloors() map[string]map[string]sweepFloor {
	return map[string]map[string]sweepFloor{
		"tree": {
			"carry": {read: 92, refused: 887, digest: "df69d17c9792c3f1f0143098ebd8b738b0fc0917e7dcf9e5278b01341ac9abed"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 120, refused: 1114, digest: "44c1e62e7b386ae599a4d5897aaa5195848b302a8e1842d15741abd7e37888ff"},
			"carry with a later failing example planted by no step":                      {read: 104, refused: 934, digest: "032fdcda2179bfbc2e184334c8668d88bc3ef5e4915101495131db51c1a4a4e4"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 106, refused: 936, digest: "02a21b8ffd6e944ea9c84143503022f005f2ce0101558a094f5eacc4c2225dc7"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 106, refused: 936, digest: "1b44fe4910e1433e1724e7f4fbedee14db3565fd69548431d2db3eeea9dc8aea"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 107, refused: 1024, digest: "0cf2e79900de905c80c5fa9817ad0303eb5fff67ad676eb1f9523bd975061740"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 107, refused: 1010, digest: "751ee5a38b72e3d986a22d5c3bc7330c0749d341017b2b0f30d036f28319a50f"},
			"carry with its failing example planted by no step":                          {read: 88, refused: 878, digest: "f283b42a642a741434946c7c795cfff8656186c8d42c538774233e20a7a843e5"},
			"earlier-text":           {read: 37, refused: 397, digest: "da552e93931654a23aee287345e4ceba1324864309bf0deaab164aece8bb6a91"},
			"one-commit-two-answers": {read: 68, refused: 724, digest: "fa77aec001dd2e37c8667f2892d3dec23711323ccbd92e41ecdd55ddd4e1ff9e"},
			"production-and-graph":   {read: 28, refused: 454, digest: "de2301de52ab209f8dbcca5450f1ec41ca0fb6750b5e8d1db3609ad23bcdee0e"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 29, refused: 461, digest: "f954bca1af95fb435df487347e136ba34d6c20afcaaac97764f916a29753ec9b"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 29, refused: 461, digest: "573c1fdd5e11aa7f56316e62722f802cd5f69284793460cd2515de428c1992b6"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 29, refused: 461, digest: "9cb94f1167f831002c099f4dbd2644ef90a814e1e19961619052da3e4a1272ca"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 29, refused: 461, digest: "f9d560ad3e9297eb760316608126f82901b724883b8264bed84ac62607398f16"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 28, refused: 454, digest: "d20a70d55d1a55b930b12201e05b0cd9956c64f6ead39336e87722204b36c429"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 38, refused: 444, digest: "0426f5fd161285abb4c45e61dcb57389d624b7fcea059d9e9f56a51bbf579f60"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 30, refused: 456, digest: "73bf5a9ea0954d50dfe6de662ee5bbad7a16bcda1ef2110e12e30655a07abd23"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 30, refused: 456, digest: "d9aac9fa1c0e385ca31b569c7ca46935bfaad663251fe10c03580699543da12d"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 30, refused: 456, digest: "5e960993fc94d0bd14a3f9e87c95f70744eb1ea539c7d4c497be45e0fadbb170"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 30, refused: 456, digest: "c0de107251d950ab739292aca0e938307e74eb2e3a270763837b56fdd78b8179"},
			"refusal":    {read: 93, refused: 894, digest: "644dc0a10c32b40cb11a01d2331b1691ed6e4279382d16e00f8ee80ab1890882"},
			"silent-leg": {read: 46, refused: 621, digest: "bdec45a2b1d8df95d39741f15d2448c8e358aede7b94ae59ee88e7047c5f5136"},
			"silent-leg with its matrix dropped at m2": {read: 46, refused: 605, digest: "41e0a77823e47fbb4332ebb70dceaf13878ce3c340e606dc755a88e16104c809"},
			"wait-cycle": {read: 72, refused: 672, digest: "fd1cd79c7a9e6199c7ea49fd62624af1cd9f5b5b77b9aa6ff9ca12d7d1408afb"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 92, refused: 815, digest: "a48624e309626fe56cc33f93e930e03ebc82e23e4e6491cb6d4ff9f901b50b8b"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 72, refused: 672, digest: "34ea446e81bf76a85364bf04bdb725f37bceb359fbd510f887658193f907740d"},
		},
		"members": {
			"carry": {read: 103, refused: 22, digest: "4c4414d881da6ba9e28c171fc3ffda6818950aab4986dc89a1af4902fcac97a4"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 193, refused: 33, digest: "ae411f9f51f9791fadc1b705a8fdfc428dc2c7952ed6fb0fe32ea684c0b99cae"},
			"carry with a later failing example planted by no step":                      {read: 132, refused: 26, digest: "df857e3b1821c60f61728b63c242942c3ed20b6b29a4fac629a54d0f0a92ee6e"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 123, refused: 26, digest: "b52ea774e843e5d0b5003fa21adf49b224ea613f7405c0ee4190bbff47611cf4"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 132, refused: 27, digest: "240350c0d78ec37b166b1df5bf4330ea16a34f680595c975db47c7ddf13f3fce"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 180, refused: 32, digest: "bbbdf2a672586a57b4b185878e4ce809ce0c9df7280397995dfdeaa6db90f307"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 180, refused: 32, digest: "3220ece34279d3dbc061787a3c8c6666ec863d9cf7890e3e935dd2528af9cefd"},
			"carry with its failing example planted by no step":                          {read: 108, refused: 21, digest: "134b397a49a912073dfa6625fd556b5829bd7782840700d1d8c3cf3812a2c906"},
			"earlier-text":           {read: 33, refused: 7, digest: "ba6a8eaf667070bc06e99a50312aa481f3407612123f08bb7f91b1c819ae4580"},
			"one-commit-two-answers": {read: 150, refused: 21, digest: "c43233bf2a7bca87e77b10fcc38b53914a4173fb716e5a7bba67081d74c7f679"},
			"production-and-graph":   {read: 107, refused: 26, digest: "92bef9d4ef529ab3417660d9d23e2cc1b58baf86a81aeb0c17edbaca9ad4be71"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 107, refused: 26, digest: "bbe964830d29f913dfe045f8f24617868fd13b944fc40ab35b6aabfeab37c5d0"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 107, refused: 26, digest: "0b542ba7c386d0950f45bd0a32ec4d63dcd14f75bc3af90918bc09b04d727a23"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 107, refused: 26, digest: "9f959fee7fc85306c9343ad1760336a6a497329d229b81fc0a1a3fcd588a1217"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 107, refused: 26, digest: "b378b56cd2c4010bda844835d21dd60e473fbf3da825f2a31a250a168651c257"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 107, refused: 26, digest: "db8070f3a88eea164173adaf2962c19d2ba22855bcf801b679e299b3028292d9"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 107, refused: 26, digest: "b18d77f8d19e6d9035f7b726ff634a1030be738d01aa8541c6ed4d208c1d4d8b"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 104, refused: 26, digest: "dc7a7f0293c0e267f064ebcb8eaa62e29c79dde4c8f01d25ff8e766e5c1200fe"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 104, refused: 26, digest: "992448248f037ce9db5caf2f73bc5a6e95a95e71092fe741f5aa1fa2aea14ab5"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 104, refused: 26, digest: "aa0d2715968f6d421a3796c5be0322061bde3221bd2470dea403c7ecf59ef362"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 104, refused: 26, digest: "d33c5ebb42c25ceacedb44a1c11bd2002727da2121962013ce07f04868b471ea"},
			"refusal":    {read: 103, refused: 22, digest: "63fe8576aaea97ba355837b0e83bb0abe2df21fcda877b72701f53e239661876"},
			"silent-leg": {read: 140, refused: 31, digest: "ea340a08a87b637e5344bfb74f55a9b69983e649153576b59c415bbf017ff232"},
			"silent-leg with its matrix dropped at m2": {read: 140, refused: 31, digest: "40fd9073c01ac70854b4ba44794a3f8ffeb4bfbda1bec4ffd9ac2499fb7eff09"},
			"wait-cycle": {read: 97, refused: 13, digest: "15c9a4e89437c4f1789d0f6cb4dd05b05680c7d7797166a0f55865ed74ca0673"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 127, refused: 17, digest: "41db25b584957f3685ff2e74a56891c5c630768ebde81577e8385a72dadd2a3c"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 97, refused: 13, digest: "6eab04882415b415052dbee88ab372d75d80f49da0609dec8a72bd26965fb7b7"},
		},
		"texts": {
			"carry": {read: 201, refused: 374, digest: "b599da2136e0924fa05bbbc6961909454a06408a9935b75fb30150d7e7b6a8bb"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 201, refused: 374, digest: "1360ea47b0710b5f2a032511d89023a2277e4ba6baee07f2206616e97d4790ae"},
			"carry with a later failing example planted by no step":                      {read: 201, refused: 374, digest: "7b8ecab2ebf8fd2ceac80c0fcfc376795c4134f00385cd7549c2d09d59eca0f3"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 201, refused: 374, digest: "ae30bd94347edf36a2d997c66f24ccb20a35db64eba75c82f0926e83a00194a5"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 201, refused: 374, digest: "7eee17401d1d532cca4d4274a1437c5ba1288e39c543e9dbfb9909e8e444257d"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 201, refused: 374, digest: "c85d94f710a32b6586727bc129ddf6439441ca21084add154479c637b4a06e7b"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 201, refused: 374, digest: "fc0ead15013e83f9435d69cb800786054dad6997c60383c8b734a0a3d9d317b5"},
			"carry with its failing example planted by no step":                          {read: 201, refused: 374, digest: "fac25ddf68c195666ea4908702b43c915908cc1e0ae6da96d7ce99db27745a09"},
			"earlier-text":           {read: 107, refused: 193, digest: "a639bcaa66b2e21e6d3b52aefd00127a579065439c4188029ab356e248c3da6d"},
			"one-commit-two-answers": {read: 208, refused: 302, digest: "dcb81e67809a4552e34018e78aead5c1e91a1b4b2a9ef48884dae6073f263ddb"},
			"production-and-graph":   {read: 77, refused: 129, digest: "a77852ba96dbcbde84328f480fe06a9be4c7e98b19c01b894ae5bdfcdaf0d8e1"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 77, refused: 129, digest: "ec303b5ae7ccb3ac9a310c48bd55b5052f9c1f41f02a905609be61f71f21b007"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 77, refused: 129, digest: "5eaea9f2e1d79abe75750cdaa825d05daaf2c7857baa9147d19fce94fc739f24"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 77, refused: 129, digest: "b77de46c77eb0a55eff0f04747aa4ff2865fdb75bbeac18e8dfd2f1ec3ae1603"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 77, refused: 129, digest: "a9499fb97ed3c4c7664ae46e920a20f5975705ff830df31cb193d11179b320e8"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 77, refused: 129, digest: "e6d802170d5f019ad00f0188b6665498d480908578d0314e0855a3b0f3a0f80d"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 62, refused: 120, digest: "55018487291ba458dc2aa8864fe10d09b68940449f33cd6a984021870c814736"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 77, refused: 129, digest: "65377e7994f939ccb4cd7601ce6d5587aa340191d87adf7a36750606de1db65d"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 77, refused: 129, digest: "5e53a86598ce2022a834f9d79ffe1cf409310c859f4679f3059a038f6fa08d6d"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 77, refused: 129, digest: "8509a1e364186d2f49397e8c4ec882aeb6426bd12f7c65cc7c1c491d04f9792f"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 77, refused: 129, digest: "ea777ccfdae0e07612819fae620883cf6aa0f2e1319b346252605471cf046ce4"},
			"refusal":    {read: 201, refused: 374, digest: "d798a1789dfebc939d5ad89d8ca683a716282e9c41fe0a53831fcf4de112def6"},
			"silent-leg": {read: 125, refused: 220, digest: "fc6843afe4b1244e55c2dea6db3ffdef612d5b6789e98e9f3ca8276c1096e8db"},
			"silent-leg with its matrix dropped at m2": {read: 125, refused: 220, digest: "f723dc866070f5be1f32199bcc1169dd0b1d3d65efa14c57e4fb00155ef9fb38"},
			"wait-cycle": {read: 149, refused: 201, digest: "a3cbb75f23289bfe73b48e11ace7fcc9d2d293a470565f403c8abbd5a249237c"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 191, refused: 247, digest: "71149826dd5544f884d72fc77fc74553d9a48e1b0c7c8d0355c61bdd9aa934a5"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 149, refused: 201, digest: "abac6ede91bf1dc7bf792c9ac857303cd6c6127f734813310fc7a8aa91cf0389"},
		},
		"attributes": {
			"carry": {read: 10, refused: 16, digest: "888f309e0da8a7affa0678df532ecbd64e341ee10016fa2547e0bdd5e42253df"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 10, refused: 16, digest: "fc947cc471f275b5c9b2386746b54da36b7180eedc0046b5ab12fe8530c1daf7"},
			"carry with a later failing example planted by no step":                      {read: 10, refused: 16, digest: "481551d78a23e4c0c437f544f64fc67e705bb42db4ec78de2fbda3b7357191dc"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 10, refused: 16, digest: "5d9c9d14ebfc3dd428f307cc1f6e1fadb028eaf6eb974f50709e22b52b4f3c06"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 10, refused: 16, digest: "a8a8919ddafff89d379c53ae71717fee91f96d1370353fef0796202705b6f5a8"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 10, refused: 16, digest: "a77ddf804804af743667d5e9d29ea1430575c9d6820ef7561789e14d46087274"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 10, refused: 16, digest: "e788e9b5e140a9d59343a35c1d79138e7bf676c2a00c651b5b41a87c4ff9b306"},
			"carry with its failing example planted by no step":                          {read: 10, refused: 16, digest: "657e1bba39fc71f566f17ff2f95f1b3d93a8380b2e569b563b7d19d546df3662"},
			"earlier-text":           {read: 10, refused: 12, digest: "3e268c7ee60194419939a4bac23a1fb98c4f0ed096025f23500e390de5b29dbf"},
			"one-commit-two-answers": {read: 35, refused: 33, digest: "73056882f792d4abb3a98d2447612219e4b798a330abd31801a558236cdf6bec"},
			"production-and-graph":   {read: 8, refused: 16, digest: "7b4b4c6dbc64f5709b07eeeb60d1bf655ec01f9fe29fb8e1a1c3533726a567e0"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 8, refused: 16, digest: "fc04c9c4f579eb9b2a8c5813da192b2bb7e4c348077cc19fcfe81da9572f3298"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 8, refused: 16, digest: "88233686c20f865e9cf5833173a3447d1e9aee32a0e241f07c0fbe35601d52b7"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 8, refused: 16, digest: "719af68ef65ad40b89dbaf8b71922a4c7eedb715a7eb09be87723b59db03302a"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 8, refused: 16, digest: "135ab20744773056cd1c66e38c29875c3cf3cc7951f8f861e666ec882e601906"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 8, refused: 16, digest: "65ff584d6510ca52e1cdf26bce1a431c787e36d2bad348a24c295c7cb97d7b4d"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 5, refused: 13, digest: "2859828bb8a27fb46e3fe05b45418e9e4c592cd8473fb58f1b2960d3e55dcab7"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 8, refused: 16, digest: "b5031af708ee9a000c8bb82d30fe2c3f28e79bfc5f39a2a0ff4c02544ce147f5"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 8, refused: 16, digest: "bbd002e08055ff9b8770ae0bc38cd4e072a3d411d0a99324ba9f4b755dce341e"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 8, refused: 16, digest: "47ebd2c1850cbc53e87d0db03013a9e6f45899c627e307ca065150383da75a71"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 8, refused: 16, digest: "eb0f1a265beab9430efa79fd5af9dc51bf5ee4a03adfd62f3daaab3aaeb8786c"},
			"refusal":    {read: 10, refused: 16, digest: "6e3ec3f55483a48fadd60894fa32223f3c81f6235287d5790bdc429ea3a66cc4"},
			"silent-leg": {read: 10, refused: 16, digest: "a20f1770964d0f805b634279aa2de737bb97e4c69277bea9376aab4455a1c764"},
			"silent-leg with its matrix dropped at m2": {read: 10, refused: 16, digest: "49eecd3e1050759a8c7aa3d3bffa56a834d5e2f31e12683d4d0bd8b2e478e716"},
			"wait-cycle": {read: 22, refused: 24, digest: "948e775b1535fce03ac3ce6bbe4aad934bbbda0b651f99e7792be127222c1418"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 27, refused: 31, digest: "790d23cb06242e60b5417aeb1d39bd2a0da1d0a64ad9652248953f82d830b907"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 22, refused: 24, digest: "8867010bc0eeca36fa7f3c81b69ebd00f1018ead59409af196ce655efa5ec9e8"},
		},
		"refusals": {
			"carry": {read: 14, refused: 0, digest: "f5cc2fe5d19a54008c968b1c758922203478797e4f820b6f3f951f05f641ccaf"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 18, refused: 0, digest: "2c2af5eb58df0827905b1c06a3cd10352fad6b2107282cc51b984bfd0d0a7156"},
			"carry with a later failing example planted by no step":                      {read: 16, refused: 0, digest: "fe7dc7372fdf3c36061aa96f0b5d6cc8433d2f1d0dda97626f1d3e4653531ba4"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 16, refused: 0, digest: "942b7646d9a0dc75a18cfccdcfa39b9dc78ddbb0979f2af4ce2c68ba1767d0f4"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 18, refused: 0, digest: "7fabb7278d2b610469ed825b5ee71c3e270165150a79f8c939f82ef39811fb23"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 16, refused: 0, digest: "5a858c1f8e97d4bccd3cd4c6a49b141b891784900bd48139ffae340e186d51ad"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 16, refused: 0, digest: "03301ea6ba59204347287c36ee92c380c7d230c3367a35f41c4deb849a57c668"},
			"carry with its failing example planted by no step":                          {read: 12, refused: 0, digest: "818bc73a144df8a1fe329b32a9a972e7539072d92b5cbe84c13b72d47f3bad2a"},
			"earlier-text":           {read: 5, refused: 0, digest: "12c21b18bb882b520480d807769c019c8069330db1fe8ca72cfc0507bbd10112"},
			"one-commit-two-answers": {read: 20, refused: 0, digest: "6d0c902347f8b6a790227cc3fc965caa79bb2098d7c8f4ca546f7e6058308fb5"},
			"production-and-graph":   {read: 4, refused: 0, digest: "dc7a42dc5385e852eff4800cf075d3d7930f817ea654709ee9fd6b2b12ddce3c"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 3, refused: 0, digest: "11da01c46deca400ab2c0f13274d7c5c43cec2513c7ece5624965cff191b7423"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 3, refused: 0, digest: "5432d5f40229c5b358907c5963965f1a3541ce1232d75b397793c2e498559dbb"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 3, refused: 0, digest: "187c2a1b605062bd5a489f72551a03e5e8d4666bd32e0dbc93116d96948512d4"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 3, refused: 0, digest: "f588bc92159af2b226869ffb0a9b6c52375ecd613705e1be21d2d6fd8fc2e4b8"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 4, refused: 0, digest: "c789e88644588ff5f0ef49f7e9d09cbade85daa803db37888b70561d73d94111"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 4, refused: 0, digest: "f2ed2357bf9f1c48130af73c87b2e8f387135d6c4afe46822b00369bffe00ddd"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 4, refused: 0, digest: "d9a52229c31fa8a477c829cb372a140387097bad832d22e3a4c1b4f25c912dd2"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 4, refused: 0, digest: "bd540971a683c912a6ac6f02eef7eab009eda392c45d101f3b15280d1ef94a0d"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 4, refused: 0, digest: "bb6820f8153ac72cead31d8b70ad0574a95ad1293bf9eeff1316c562ba75c01a"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 4, refused: 0, digest: "5c192f0b2b514da8dc49aec41ea197079253bdfce41c11ecbebb82cf128a1717"},
			"refusal":    {read: 13, refused: 0, digest: "fd1fca34b8c545605ec10f082a040873fb53e361eae5beaafd15213bb480f287"},
			"silent-leg": {read: 10, refused: 0, digest: "a95769a7561158052c52522b56550175f6693b3de6b6a887edbbf11daa2a02bd"},
			"silent-leg with its matrix dropped at m2": {read: 10, refused: 0, digest: "e6bea70eaed6c34a6795609943144e331e39adf594e6d06adc2fcf7ce9ac782f"},
			"wait-cycle": {read: 18, refused: 0, digest: "ce8e26be0b571412845b35fc77eeb8c53dd0f2aeebdadb75209f4ceece3dcd1a"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 24, refused: 0, digest: "4b9b39b491d9aa92935dd8c2e526659497c5746fb89e7afc5070b57b66a3892a"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 18, refused: 0, digest: "4015f0f591b38c8b81b071af0ab859d49cc1483585ead9f3c634b261bbd1ed95"},
		},
	}
}

// sweepFloor is the floor of one sweep of one base: the inputs that the reader read, as the base or
// as another state, and the inputs that it refused, as sweepAgainst counted the inputs that it
// judged (sweepCount), and digest, the hex sha256 of sweepCount.judged: the key of the base's line
// (readerLine, the sha256 of the bytes that the reader's process read of the base), then each judged
// input's key, its kind, one byte, 'r' read and 'x' refused, its right and each place that the right
// allows with the state that it allows there (counted), in their order.
type sweepFloor struct {
	read, refused int
	digest        string
}

// markReadingsFloor is the count of readings of a member left out that
// TestAMemberLeftOutOfAFileIsReadByItsMark read when the floor was written.
const markReadingsFloor = 82

// atFloor fails the test unless, of the inputs of the base name that sweepAgainst judged (n), the
// reader read at least the floor's count and refused at least the floor's count, and the base's
// files and the files, kinds, rights and allowed places of those inputs, in their order, have the
// floor's digest (sweepFloors). It counts no input that the sweep built and did not judge.
func atFloor(t *testing.T, sweep, name string, n sweepCount) {
	t.Helper()
	floor, held := sweepFloors()[sweep][name]
	if read := n.read; !held || read < floor.read || n.refused < floor.refused {
		t.Errorf("the %s sweep judged %d inputs of %s that the reader read and %d that it refused, and its floor is %d read and %d refused (held %t; sweepFloors)",
			sweep, read, name, n.refused, floor.read, floor.refused, held)
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256(n.judged)); held && digest != floor.digest {
		t.Errorf("the %s sweep judged the %d inputs of %s, in their order, as bytes, kinds and rights whose digest is %s, and its floor's digest is %s (sweepFloors): the base's bytes or an input's bytes changed, an input moved from one kind to the other, an input is judged by another right, or the sweep judged other inputs",
			sweep, n.read+n.refused, name, digest, floor.digest)
	}
}

// floorsCover fails the test unless the sweep has a floor of inputs for each base of bases and for
// no other (sweepFloors).
func floorsCover(t *testing.T, sweep string, bases map[string]fstest.MapFS) {
	t.Helper()
	if got, want := slices.Sorted(maps.Keys(sweepFloors()[sweep])), slices.Sorted(maps.Keys(bases)); !slices.Equal(got, want) {
		t.Errorf("the %s sweep has floors of inputs for the bases %q, and the sweep reads the bases %q (sweepFloors)", sweep, got, want)
	}
}
