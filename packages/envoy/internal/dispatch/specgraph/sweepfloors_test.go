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
			"carry": {read: 92, refused: 887, digest: "87b29453f977c91c27cd3e246e04f8525af7f57558484824414c5b2f325bd037"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 120, refused: 1114, digest: "13f57c240fc63d2e6ab37361cfd8c6093e284f12d706964e7b54b7a142f9ecde"},
			"carry with a later failing example planted by no step":                      {read: 104, refused: 934, digest: "ddd859ed3a75f83121c2da66ae6a0063376125b336aac895b162b48295462221"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 106, refused: 936, digest: "8509b1b838852cdd648e73d658c002c3795f0d1a061509c42bfd38e17c9ce361"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 106, refused: 936, digest: "d7b67287ac9860d037c7331964b0524806b10661f496bb06c037a6a0bcfbd51d"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 107, refused: 1024, digest: "c7c00d1dbc5a231658a99ff5e544d1b5e6c95f5b26e7009fcaf9b0002eebb721"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 107, refused: 1010, digest: "d395b4a062c0017254fc9055538e1cca7c298bc296f994498408359fc297f6e5"},
			"carry with its failing example planted by no step":                          {read: 88, refused: 878, digest: "69b2462d645aaa2ec7a6481115e46031a1a168828eb1aecd7e1780181f95ebc9"},
			"earlier-text":           {read: 37, refused: 397, digest: "fcbd228889d24d145ec1ed0cddbff9bc86398dfa341027cd688b88e46587e058"},
			"one-commit-two-answers": {read: 68, refused: 724, digest: "9e73870869d0ba79d00e45cb3949ebd8e910f623d50a58b3bf29e4e447081c02"},
			"production-and-graph":   {read: 28, refused: 454, digest: "29b1c2ee99773b85e880cf40d5a53c155f443c83ae859835e7d0f73227b49a19"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 29, refused: 461, digest: "401bad9444cc3f34bf889936b5cdb48abe2328e606d518835a003e8ff0da739b"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 29, refused: 461, digest: "d55d0bc61ea8fd4a843cfbdd2de49f0d2e0c5dd4fbfa37274038566a6390f74c"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 29, refused: 461, digest: "1d8acd68bb9496b2a857de0826b3ecfaaf35c8e23c7211fc2609798d697a7237"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 29, refused: 461, digest: "c797a8c5a4de79a4ce4fcca72da9da9e3468efa656f0000672131ad4b0236a04"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 28, refused: 454, digest: "8ca44a230c82f8d443354ad5104cfcc456f72d5ea5ab96184ce04b629f18ac5d"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 38, refused: 444, digest: "d85cbc9715c483e6c6bc3bcdd575badf0b4e053749e417bf2fa71a3437aadfa9"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 30, refused: 456, digest: "b9450eeae0dedde4dd9339f11003d1d77ee7760fd376ee344a0cb45301651377"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 30, refused: 456, digest: "f259396cd9d9f9de577ed678944f04624c28d4bc27b44ba6dec60c7d33793489"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 30, refused: 456, digest: "443dd3704e2a60f204ac7b65ff24f61ce01ac9b6765ce476287eb639762fb4dc"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 30, refused: 456, digest: "40c27aea4b06084a6b58c24ba4284f59ba0f951d2c5474dab9a01d0a09d9c29e"},
			"refusal":    {read: 93, refused: 894, digest: "fc10a3afb82c67e2287a38afe7d235d6503288010c172a17bcc7c3565fc622a2"},
			"silent-leg": {read: 46, refused: 621, digest: "2da347b7286aede264a75949711c6d8ada84eb9af566a198d42c28cefb3f2cdc"},
			"silent-leg with its matrix dropped at m2": {read: 46, refused: 605, digest: "0b408aa004fcc13e8b63b2a25d79c8fb694af2ab87290c2d4fc536223610eed5"},
			"wait-cycle": {read: 72, refused: 672, digest: "24b0e731ccf83c914d4e454ccda676a534cbbba3f0305ba9eb0b5d906aa54192"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 92, refused: 815, digest: "e0ceba16215f4d2c4064ec13bf01b1ef653b9bed43f9ea58ddfd8aec424ced94"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 72, refused: 672, digest: "a98c7a99ca6b38db8c206b9eb65d7e8490a7cd9d89b1c8e20d738be5c11fdf5d"},
		},
		"members": {
			"carry": {read: 103, refused: 22, digest: "0dbc891d088cf0643f978e40f5c7d702a8a68684eabe08077d9f060d4274cdcd"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 193, refused: 33, digest: "4dc6a10c3f6a1e181aac8c154e04b3e64485973500c83a0a0fb8bb4ae2bb285f"},
			"carry with a later failing example planted by no step":                      {read: 132, refused: 26, digest: "27019d7925ef1c5ae3aa951958f23a070a4433fb9be0e795363b1c48187436e0"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 123, refused: 26, digest: "a772e5fa766e1688d8168c6f3ecfdf9ea744bed7beefa984bed28e187fce6619"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 132, refused: 27, digest: "8f716135c215c4d29e4e7866b921fd93ac6c66a2d9a76c7923bd2815f24b9a2d"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 180, refused: 32, digest: "ffe8818bf761a2e2740fbfea76a9ee7494b3a03cb72a8726f95195eebcac238b"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 180, refused: 32, digest: "8940905737addfe342dcaeb776abf57816538e14380a7ed971ac1296debd4b08"},
			"carry with its failing example planted by no step":                          {read: 108, refused: 21, digest: "3f337761f184c22e17b3c1779479af41440f9a09a05edf18856187e630786cc9"},
			"earlier-text":           {read: 33, refused: 7, digest: "813da21dfd417904590c7087648b85b6c1463eec056dec3ed49aa66293b35515"},
			"one-commit-two-answers": {read: 150, refused: 21, digest: "6d33f82dfee7601b5f365c1a7482f15518e71f1448d70d61bac437e66d09741b"},
			"production-and-graph":   {read: 107, refused: 26, digest: "83c597ee2ecb3a4dd3e8d12ddf7c1561fc644210b7c1326a36254d1122524f76"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 107, refused: 26, digest: "985d788ef631e8297de05152663b35d62d939f68f8a4f59e74c8adca5f955be3"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 107, refused: 26, digest: "350848e7d01856a84cab403f33fd09976b4de246406c20b592cab440b5f6df14"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 107, refused: 26, digest: "0f06ec2678ccf5a5281bd783a01bdc61acd6eb53ced390035bf56e9adb990003"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 107, refused: 26, digest: "fa97da2309830a801b135c2919f5bd675e9dc29b2abbcdca9d8b8f6f678133de"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 107, refused: 26, digest: "f884cf04eb89b5fda3a5fef6b5578c8d6b146dcd41ee17cbf0e2474993a5bafe"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 107, refused: 26, digest: "2126c12889f7837c01979bd9a284156d14fef8c43b0350733f0b14ec164e9bc2"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 104, refused: 26, digest: "1554317caafc4281afe8c204a724b12932a34a7684c55d9aaa132067b8667a4d"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 104, refused: 26, digest: "60bf7b6e856965f52e0f6124fb0c12688e14c561da327051646aae23d2a09370"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 104, refused: 26, digest: "8272948757ee4afe988c342e03a719b3720d9c7f8d9f31b59ecdcd4be7cef39c"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 104, refused: 26, digest: "6fee1b668baf08b29ff5e644cb51b8e27c9e40a760eb20aed4d643b7728cd9a0"},
			"refusal":    {read: 103, refused: 22, digest: "ecf6b71d02a7677f0bb88366fdeffaba38a0d9130afd3ebd9f7bf3e778f44b11"},
			"silent-leg": {read: 140, refused: 31, digest: "7b2bc34e80c67078a9342e8f235535513ea494898ff0b14514ba9028ce127b82"},
			"silent-leg with its matrix dropped at m2": {read: 140, refused: 31, digest: "7b820841834819ca0fdf9e65f66028eec4d56f512ba436310331086aaad3a126"},
			"wait-cycle": {read: 97, refused: 13, digest: "78c5ba979aaf3c225da699c22e2047994aefba15e6402167661cfd3d5e0e57d8"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 127, refused: 17, digest: "4d8b5a4278d77223b057065d990aa6887fc2fae67f7a458674820088db85a10a"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 97, refused: 13, digest: "0550ab781a9d3ec011775ce71a66d0d6e00d8d330ce8c37da66eb0ca60bc4ece"},
		},
		"texts": {
			"carry": {read: 201, refused: 374, digest: "fadf8ba90c634a36e53b515d2662a649430d101870024a07979a0fb330fd5942"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 201, refused: 374, digest: "7fc19a4fc46a61fb3f8b6b0a09c9656f1ae5b5417ee4d0b19f7fd30758d35dc1"},
			"carry with a later failing example planted by no step":                      {read: 201, refused: 374, digest: "65d65b0bcee20e4220e55a38178a8d85ab45993504de6810b74a701ee08658ea"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 201, refused: 374, digest: "30b92d411469e679022ae1f2c7c6b0977d12e66c8640fa8412497f8b8380e174"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 201, refused: 374, digest: "3548caa9c193b69ebdd998e32c7eb01fafa7280e94e1df8abeca076d784f3dfa"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 201, refused: 374, digest: "4ecafbfdd8c0fe88b7ef42916d623ef1910aaa74fbb463bd3794f385c097fd1c"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 201, refused: 374, digest: "94a8fdf00619c7b0e5bc9188a4f0ebd71f8d9c89b92884b94934d8c578dec2d6"},
			"carry with its failing example planted by no step":                          {read: 201, refused: 374, digest: "208234aacb83202d76b5d307ba6aafa092d0b86647c43d5443fa71b2630dcfc8"},
			"earlier-text":           {read: 107, refused: 193, digest: "e20bd3a22164b01b520cf1bcc4018cc0bd101d768cacbe329e0ab089e8034f7b"},
			"one-commit-two-answers": {read: 208, refused: 302, digest: "17d080e6421dd2294d2187c9d5044bda8b73814a82634b8a82056a2c867194e7"},
			"production-and-graph":   {read: 77, refused: 129, digest: "b34c1ee54f5b523ba324c4d8f63941c363fd69984b059c595211be3759e6096a"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 77, refused: 129, digest: "3b7800e133d49ae9b8e3786f3c22c1546c35a1e5a7c43b780ceadc4503cff661"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 77, refused: 129, digest: "a2af9140048a2f7b74fb6f0cd3b2779f72dcac4c1a2c6ad036bbfacce931d781"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 77, refused: 129, digest: "67f255a912c50f809f17ebf6239aaad495681ea5d3cc3496598ed0d5e898265c"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 77, refused: 129, digest: "8594cf836f1ac56c4103b53e0095cb10f6d9f0886e2451595a480b42db160956"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 77, refused: 129, digest: "8d816cb654c52ee5ab7a4f57f28b1da744bdee7f3c502e700242d6cb1556aab6"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 62, refused: 120, digest: "5b1413e0b4a0bf4930e1d454ecc27233e6f30cfb9fd2eea9ccfa6b631e1930a3"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 77, refused: 129, digest: "98239fefd64d1462566a884b3891a464e4de26e01a4bab7005718692fdeccf3a"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 77, refused: 129, digest: "49664759090d4ff31d49a69f7c046ee6a7469830b6db2de866e2779e555775cf"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 77, refused: 129, digest: "b8a7c4c58e5727cac976427d389081ea347812d466bfcf4e9c7397ac14a758eb"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 77, refused: 129, digest: "81983fc9920170ad6454cfb88c14313bd34af8da9f84dd2e3d1d3f3e8d7928dd"},
			"refusal":    {read: 201, refused: 374, digest: "7512cd2499675bce6fdba4e4e731c51869715e9e7522278bdd943226ef1f17db"},
			"silent-leg": {read: 125, refused: 220, digest: "3badc9fb435977155241a41f785e68adbd0dccfcb77baadfd2613a0654c0bcf7"},
			"silent-leg with its matrix dropped at m2": {read: 125, refused: 220, digest: "a457fe7b71a2acb5dd1d75012dfb8fd809a8d4183228cfb63cb0a28bac68fd9e"},
			"wait-cycle": {read: 149, refused: 201, digest: "d7e402ab53201ee571a8ac9ffbcb6a0cabd9c1063d662e0b58311c8b41cbf760"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 191, refused: 247, digest: "2ac15c0276399c3e08b68ae881f59729e8d883b8b00b1ff5f4cd5652bb0db812"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 149, refused: 201, digest: "e4804dfecd37a8447cefc7f92dbbc9ddf339299f7a9064a95b4238f0eaff0acc"},
		},
		"attributes": {
			"carry": {read: 10, refused: 16, digest: "f2787ea02e438083f0e5c5f2edd2e9e1558f07e5cb9f575b7c5e39cea2b3cefb"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 10, refused: 16, digest: "f1038d67ced46889ec87c0aa3a76003002be1eca18bd896c432eb7df80cb5b00"},
			"carry with a later failing example planted by no step":                      {read: 10, refused: 16, digest: "b5082a929b7971cc335919fd657427b2b1ab4c9488edc2cea66211e608a09c2c"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 10, refused: 16, digest: "b86ef3a895489e1404a80fc5562d57710fbae2e5d1ef0debd80df37b0e930b13"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 10, refused: 16, digest: "cb81280d6722732e8e6944c33cdcc0d9c79a045fa289790340771809c824f169"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 10, refused: 16, digest: "552ca6ae7d7fd8d463e93882a69936540c4164269d256ef993f0a321038c1a93"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 10, refused: 16, digest: "0573c4451fce0a123c27f5d6b76b0df24e271a5a5ccba84e22e2f9253e926d88"},
			"carry with its failing example planted by no step":                          {read: 10, refused: 16, digest: "7ab80667ccae2c17aeab1fb2c59c96bf390027bcdc4380113aae30422c2e96e1"},
			"earlier-text":           {read: 10, refused: 12, digest: "91d86d437384becb80724e78d8fe3b990daa209ac3e68eb0b0b29ff907e5bdb5"},
			"one-commit-two-answers": {read: 35, refused: 33, digest: "1e258e405b92ae5cedfdd927e5d8bf1a2bc201929c5ae074f0da9649c58ccb74"},
			"production-and-graph":   {read: 8, refused: 16, digest: "10134141214b1e74ea437b11b19903aab9c9ccf9744c543890bb27ebb1174ffa"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 8, refused: 16, digest: "10e27538f32903434a46cb6ad42da7a73e67bf717e6f319b72b7f2d6d1da7ffc"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 8, refused: 16, digest: "2f7c1111df717a37fdd26d13bfb39badc560e3c8ae59d015ce9fc27062dedec6"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 8, refused: 16, digest: "454b4d2a065f9f61ce0c24e82120c2ce8ba624bbaf44c1e80f670b0a00d567ea"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 8, refused: 16, digest: "7ad54eea0df3cbeeb3ffd51918e1b15e93c683b1416132e52f8ef5d633aa1154"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 8, refused: 16, digest: "2a781560b442cb867edfc92e8cc64bf4c9d370ff885a98fec0bdd9d3d0e261d0"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 5, refused: 13, digest: "2cdd4af77f3c8643f3245fd857ec70808f08f688410529b98277465c214ad54e"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 8, refused: 16, digest: "bb9a46ce02d92e58ba2d9db14152dfec16e3ca933806a66cf8b0d6bb97130132"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 8, refused: 16, digest: "1aeb5c8607dfbe24d6596c8e2b95be381a39b28f6c3c472dcab91042d2df2af8"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 8, refused: 16, digest: "29c58b4b5e06f73b4ccf09d562c750cf3203b9a1a1020269fb4d439d385a3dcb"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 8, refused: 16, digest: "ad0fb9ca73261a863fb0ec349f2d2c9bd6cd3418a9f200bca723ca411d44eb42"},
			"refusal":    {read: 10, refused: 16, digest: "58d2ba4068a87c913559f2936d3b0bb62d1472092baa7c2b0eebad778e67dd0e"},
			"silent-leg": {read: 10, refused: 16, digest: "72a0695b04ada430afc99130c34a3b444dd2cd51271a3dc0774a172730e17902"},
			"silent-leg with its matrix dropped at m2": {read: 10, refused: 16, digest: "9cac52a615af83f7218331ee17a62991785aa3b98c0c3c78c5e5d0a22728ba4a"},
			"wait-cycle": {read: 22, refused: 24, digest: "c98f7a59a50b5035e3ae6b83bfee43be0b683f187c1a9f49e74383a6ff659961"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 27, refused: 31, digest: "fbf3221b8945ac31bc15c67b389e90be22938ca99d6f61b3d9c6f3e986130051"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 22, refused: 24, digest: "a1c32c605fd5dd783b09f89d02957923fed5d5ba1879e2394ff33cd3c8d4e5e8"},
		},
		"refusals": {
			"carry": {read: 14, refused: 0, digest: "fd9e42d319c2ebe9d689cc09581859e2eefb112d2c68557c9022de0802875570"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 18, refused: 0, digest: "f99f67d4fbd9f31163b9599a53047396bb26cfeab5cfa956579490c0edc1ef31"},
			"carry with a later failing example planted by no step":                      {read: 16, refused: 0, digest: "02b756b4a81eba9cf5096bea52e02c4db0c3a54abdbd57391bbb732efdb06860"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 16, refused: 0, digest: "e0ae50fa29f449240ca9f0d86fa4e49ebe1989d813fe1f8215644a3a9d4899af"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 18, refused: 0, digest: "bdcce3e0e0c73b052ec70b779398cf081a8293c4d33e445f578271c4a7d0edc9"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 16, refused: 0, digest: "6ca244a53288cdeae256375bad60616299b25d1985b1f18c2e5c7867f5a31690"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 16, refused: 0, digest: "6602af2f7d7137a510d171462274c0f5e2c6092f8aa5593e30daa2e46649c75c"},
			"carry with its failing example planted by no step":                          {read: 12, refused: 0, digest: "582bcbb90dafd91cb4ee7a54bc5cadb05aa11779bb12e141a1e324cc220685bb"},
			"earlier-text":           {read: 5, refused: 0, digest: "7e47f56bb8d7527d3557f075071f199c5c58c6f101b0fd2ed2dd5bb4b6b856d1"},
			"one-commit-two-answers": {read: 20, refused: 0, digest: "d3366f1fb2b99087b51fb4a0601628d0c7e75e6550a68f9130a0b3ddb06d77d2"},
			"production-and-graph":   {read: 4, refused: 0, digest: "ddb52bf4d90fa2d206ba7201cc95445cb5d0f704e369e73a859b73c67c81bee0"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 3, refused: 0, digest: "1d80b1cdc76d58873c3197b9bd1bb1e79e42de9840a5e1d0b551a684d078a066"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 3, refused: 0, digest: "ff1de4f956a932af56796e59d4e60502404ab97d06fc7a4eb02529284ac11861"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 3, refused: 0, digest: "0faff13592ab44f1d0d041a6bde38ecbf58de16b748eab81c05e4be29a55ec8d"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 3, refused: 0, digest: "e8c838977723762fb9de112c89c09aa41ed12c6bb7b520d18c8dcc5d4ce4f59c"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 4, refused: 0, digest: "1b50d5618ba44fae5d8c5bf6a697ee63d8793f1e6303c1937cb4483c477580e0"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 4, refused: 0, digest: "9e4170cde7ca0a75bf840e9b4316641390386f9b0291e457ba1ce552bfd3a631"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 4, refused: 0, digest: "7be8ae5fefa4a877f41395ee6d5ef42b2abca48573c450d98c635055f47225f5"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 4, refused: 0, digest: "b023bc8f927ca2d51a7dc9998a79428c850452d8cd3ae712f31e3a6b215afe27"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 4, refused: 0, digest: "de7e754555f665d89dfe95c959423efedfe5d68a2109fa453970f50ac60aa647"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 4, refused: 0, digest: "70506819884f2414f9a78643e2a889a191ad10e537ccf8353c1c3480f1d95465"},
			"refusal":    {read: 13, refused: 0, digest: "9b6e04263acecac625b7969850f587719cf536502dfc09f11d26e65ac84d342a"},
			"silent-leg": {read: 10, refused: 0, digest: "0bff9024a75527d8f88e879b462ed6e1e4fcf159ea2bd6270ab6be3de6c847c4"},
			"silent-leg with its matrix dropped at m2": {read: 10, refused: 0, digest: "0ae3cceb43313536c2c55112999b9e7c8c32a62ed99fa750c59f12f6a8288233"},
			"wait-cycle": {read: 18, refused: 0, digest: "9c2a8e86ffbf95d3860bb032e40bba275f0bd2ea3016f92ca03a9403e2604fe1"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 24, refused: 0, digest: "e9a76339e7b0ba6725a51c261fe12d83b31573fd4b8d407273334132b9c015c8"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 18, refused: 0, digest: "17dc3f971e56f2b67f10f6227eff93017b3413d0bd549bdc5e05c963f076dae7"},
		},
	}
}

// sweepFloor is the floor of one sweep of one base: the inputs that the reader read, as the base or
// as another state, and the inputs that it refused, as sweepAgainst counted the inputs that it
// judged (sweepCount), and digest, the hex sha256 of sweepCount.judged: the key of the base's line
// (readerLine, the sha256 of the bytes that the reader's process read of the base), then each judged
// input's keys (for a read input, the keys of the lines that the base's reply and the input's reply
// that the judgment read answer, as it gives them back; for a refused one, its line's), its kind, one
// byte, 'r' read and 'x' refused, its right and each place that the right allows with the state that
// it allows there (counted), in their order.
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
