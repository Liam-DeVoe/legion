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
// another state, or refused it, and the digest of the base's files and of each judged input's files
// and kind, in the order of the inputs (sweepFloor, sweepCount.judged). The sweeps are the tree
// sweep (TestNoInputThatSaysNothingMovesAStateTowardTrue), the members added
// (TestAddingAMemberMovesNoStateTowardTrue), the texts inside a block and the attributes left out
// (TestNoNameOfNothingInsideABlockMovesAStateTowardTrue), and the refusals added
// (TestAddingARefusalOfCIMovesAStateTowardTrueOnlyByTakingOutItsCommitsResults). A sweep that judges
// fewer inputs of a base of either kind fails by name (atFloor), and so does a sweep whose inputs of
// a base hold, in their order, other bytes or read as other kinds than the digest's, and a base
// with no floor or a floor of no base (floorsCover). So a fault that drops inputs, of a sweep's
// meaning or of its writers, a sweep that builds inputs and does not derive them or drops an input
// from its judgment, a fault that moves inputs from one kind to the other, as many each way as it
// likes, and a change of the form of an input that keeps its kind (two spaces for one) cannot pass.
// The floors count what the reader did with an input, not what the judgment found: a judgment that
// stops reporting an input that moves a state toward true, and counts it as read, meets them. That
// judgment fails by its known positive (knownPositive), which each sweep judges at silent-leg and
// must report, and which no floor counts. A base, a scenario, a sweep or a reader that adds, takes
// out, moves or writes otherwise an input changes the digest, so the table is written again, on
// purpose, from a run of the sweeps. It is a function that gives a new table at each call, so that
// no writer can lower a floor (sweepMaps).
func sweepFloors() map[string]map[string]sweepFloor {
	return map[string]map[string]sweepFloor{
		"tree": {
			"carry": {read: 92, refused: 887, digest: "881490dafbe6f459f69f97e2b14fe9699f0f7872badba48e805165d7cf1cfc38"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 120, refused: 1114, digest: "deaf829fcba6e394957ae4bb1f41079e4b3154aabb4e2b139342eff42a21f47f"},
			"carry with a later failing example planted by no step":                      {read: 104, refused: 934, digest: "09c9478916438a758bcf4f87e3e0a2727f4fb00c3dc5911c6ce9d448e42d5521"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 106, refused: 936, digest: "f0ddb490b6219db459142d0ed359a4fd1757a4b1d5045174bca5ee2a9116dac1"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 106, refused: 936, digest: "3016894de760923a6df311a0b89eb751ceee4b8265cf212144d71b699e1bd11a"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 107, refused: 1024, digest: "dc5e41570b0c0a2bca35558d6cd6d56180f65837d403ecbc5a0cd3b366d3b8cc"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 107, refused: 1010, digest: "4f4a856569b16843c2f9dde2d19635f7ecea53ace26523394e62b17a2d3ffe5c"},
			"carry with its failing example planted by no step":                          {read: 88, refused: 878, digest: "8a473f96c1c05df2ea7f69e14dbc28acfa7922c3c03e0524f418fd9ee7b81a90"},
			"earlier-text":           {read: 37, refused: 397, digest: "aa83a20ec88a0d97125dd656852bf90fe4d9266cd05ee9163ca1a3c55118b4a1"},
			"one-commit-two-answers": {read: 68, refused: 724, digest: "3f92b4df90cec33b70c45b5cb3e3e765f6fa354daaa7d7c746d88a0bb81fe005"},
			"production-and-graph":   {read: 28, refused: 454, digest: "12e2973cf214a79a16b77f95d4abfa420bf8c5d2594286568002fa2a214213e2"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 29, refused: 461, digest: "eb603aac4ff41f0ba494b04aac8a6253e1eb9f60a0da7201f4cbf8a489bc2f8c"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 29, refused: 461, digest: "0675363e7c144f17a741a5c22716121129d670e995d75dfd22f0e37dab4e60b3"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 29, refused: 461, digest: "541fb7d4e7d76b28193c961b04a5174c527a1ee1d9cbe80713e2696767e95da2"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 29, refused: 461, digest: "af9ebcefcaadfaead3b5aa167ad402084fac6644e3de25f20249a6cf5843d79c"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 28, refused: 454, digest: "9f9d5fa9067b022f7c6cce5cafa8c64b8f75d0f8a541bed9e2b2ce05137dcc81"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 38, refused: 444, digest: "44b33c9bcbabf6803cd1c9fa85ee57700c3435f467eee61dfb9d211cc03ed028"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 30, refused: 456, digest: "1a75e1db96ba9e24f38a1148c904adab03dfa24d1adf1fb5e442a1fd34fa4de1"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 30, refused: 456, digest: "e8c0688b6fa97292327911a52bd40af071be84c9aa4541df5de2b4230b1b86d0"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 30, refused: 456, digest: "0d87df8ce13465a0889d4fd669119fe8ad5979198ac5f78c1cc7aac7db40689f"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 30, refused: 456, digest: "15c2380dd01f48788340606fe545d0626e221dd093f00dd86c2334893cec659f"},
			"refusal":    {read: 93, refused: 894, digest: "12b2fe4923133e37d87909e3cf6785499297ffe5662f761b33506d8f9868da09"},
			"silent-leg": {read: 46, refused: 621, digest: "1bf05ee45acc0df9d5ad47e329b547dd3398bc4c749a1d376426d69cf0263785"},
			"silent-leg with its matrix dropped at m2": {read: 46, refused: 605, digest: "32f539f3b8f837aca96c9af7c95775f4d44238cf7b45123bf97b93abac9b56f4"},
			"wait-cycle": {read: 72, refused: 672, digest: "16cd33855196f809974b28e49a2f5bed33abe0aed1ca83d6270d0d0699ab55c2"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 92, refused: 815, digest: "f2f67773beff44edc2a05cf702795c3900ef528f15fe83feaa61ed9bf21275dc"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 72, refused: 672, digest: "fba929e7d9166da16e1a1e7afeb42be9a2ede18bb1475783cdcb836a8028eeae"},
		},
		"members": {
			"carry": {read: 103, refused: 22, digest: "374047abb692099c17d09df62e677eeac64134ff7d8140ee0d067b6d6093921e"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 193, refused: 33, digest: "a76d56fdcdd72a3ecbc34111731c93519a91b2bb6a72e499bd9c0beeb9728793"},
			"carry with a later failing example planted by no step":                      {read: 132, refused: 26, digest: "43f79cb160ed35c8b6a1edcac39a28be548f91d85d054f7ca10ad39f571c8bf5"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 123, refused: 26, digest: "a478e5f4cda98f8b24803289ff4e2890a32bf15ac9eff874c925413d698b23e0"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 132, refused: 27, digest: "3dd7ce519cd230666e4cb19e833740298a788f7388ac83a5b19cb3b48404670e"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 180, refused: 32, digest: "35fff7cb77cec27a6daa7b58298ac734be11581d79fe77c6f28f530a3852ffc6"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 180, refused: 32, digest: "79976f69565faa1e2e992247976427178209d3b8378b4f226a1410e0605b39a3"},
			"carry with its failing example planted by no step":                          {read: 108, refused: 21, digest: "2fc807c5a91610f4257fbd1702a2c9d1ea659590b92cec8b81ecddf43c113279"},
			"earlier-text":           {read: 33, refused: 7, digest: "5ea1f507b52c15856bbf99ff91791eedfdcf06b4903b34cf6fa93316de59f124"},
			"one-commit-two-answers": {read: 150, refused: 21, digest: "d455877b24ecae424e6f9e6ba89f9e154a7e64e7945262f37030611d1b1e0eae"},
			"production-and-graph":   {read: 107, refused: 26, digest: "4ef216037632e9f208b7dd062d4a95032ff1df1a5f82ad3e0dce5129a83109c1"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 107, refused: 26, digest: "17db6e523420928e9e9267393bb0d48a047d78641e08f6c05f19802836d5b8da"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 107, refused: 26, digest: "c4fc0e9b4e08c47b648ba06d86e20330d0e06076403aa92c3259216396b6d4d2"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 107, refused: 26, digest: "e0ff0ab62f950c7dcebf924a219184d5557bbbbf523000929375416c666f2695"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 107, refused: 26, digest: "5a28ecf3c8367c0f2b324ed7c614b0b9bde33c4195b72c2bfa4795d7f6e6b7f6"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 107, refused: 26, digest: "7f7a59a7d615cb084fed220d042f6b16e091fa305571b0723d13cec3f2f7884c"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 107, refused: 26, digest: "c63a0b8100312b701bcc57da108898f677788e91daf5f5f7a90e4bd8abac8e66"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 104, refused: 26, digest: "48c05faa699b1306878c1e19ffee5fbb1caa5ccedd4279182fc3b16b002ec742"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 104, refused: 26, digest: "5a55da0da7a93b8d01f8787739bbb297650c6277979d7d50658f63896d09eef1"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 104, refused: 26, digest: "bb9f9ac7ed2681ead86c50bcb3cd81aeb5934b766ff94ae2efe9fe0a5c614642"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 104, refused: 26, digest: "b836ead13149e57758b0569c2d152898b14d9a62eb24b0e18e47dbd350d3f921"},
			"refusal":    {read: 103, refused: 22, digest: "46179f0a3dacc9f04af777e545227045f97844b4133437c6572de44987c333fe"},
			"silent-leg": {read: 140, refused: 31, digest: "64093d0ffd8c380de38a339be87e30b5059bac82334d44191fc697229a643f23"},
			"silent-leg with its matrix dropped at m2": {read: 140, refused: 31, digest: "7470c3b20f0558ec785263499e291033690f3469866ae33d197d82a78343f06c"},
			"wait-cycle": {read: 97, refused: 13, digest: "2d52f91731c3f77951aa2dd9e91530eaf59226b9051c6ed577a88ada0a5b62f2"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 127, refused: 17, digest: "8aaff4f359efdf932491521b836b085cb9f0ed47c99e67a98527209b6d7c4240"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 97, refused: 13, digest: "4acbc36ef9190e9342e4f7e04145f8e90e408d980d17b8dbf092be2b11256b71"},
		},
		"texts": {
			"carry": {read: 201, refused: 374, digest: "71975c780fb34b095e071bd5ebb1afd1cb23bf0c128e9697024464bc6e469d58"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 201, refused: 374, digest: "21d93efe4dab176cdc3f0d05dcdda02ccdc8bbf4b896749c5d0e5ce7eca2c283"},
			"carry with a later failing example planted by no step":                      {read: 201, refused: 374, digest: "f3e8886f31a68de888de57556a6c8ea04b1f8b7059984f5c5858acb9596d4a8c"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 201, refused: 374, digest: "93ccdaabb198cda91a45379f244ca2d32ada3bf89c262a877da2630e903d803c"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 201, refused: 374, digest: "2a6c708bcb6d5e7077af0c70fa2a8ee0f254a4258287af3f7a21d5c2299a0518"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 201, refused: 374, digest: "a37d999202dace9b1de04b9c042f047b83b3e6ecf9054259f940eb9a8a398ae2"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 201, refused: 374, digest: "be736b8e246d36d0a2fa069c14af9e73013fe3080c2696d93b00164fbd4170cc"},
			"carry with its failing example planted by no step":                          {read: 201, refused: 374, digest: "a5c453641af323452bff4bd9dc951f7517ff5f5f199c29971b06c3f8d6dc7fee"},
			"earlier-text":           {read: 107, refused: 193, digest: "336881e3f7f58d68aedf77191e0775abf348915540e01f4d79c092c96348d005"},
			"one-commit-two-answers": {read: 208, refused: 302, digest: "dc334d6a9640b9c4ba51a147f34e409273b312f171ef65f6ba58548b675c49f3"},
			"production-and-graph":   {read: 77, refused: 129, digest: "d2c458925520750ab91c9d74cd6f2beb7ecc9a45c0e89a80837b9eabf1474b90"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 77, refused: 129, digest: "d55b1e56dc14e71fad461856e259943b8904c2fb65804ace859260cc79c6982a"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 77, refused: 129, digest: "90104aa8def37d58ed85b27af9afaa8707f27575a307ff3c4c1ef21c9d9e9877"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 77, refused: 129, digest: "b51ff5768e8f033e6bf3d0b43eea0a249c19cd789c4683ab737b164072478972"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 77, refused: 129, digest: "1ca8964c3bbcc5e2b405ba63b973160cb7fe99aa28269dde5515d9457624b653"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 77, refused: 129, digest: "20c7d3c5d8bfcc9daebf9bd288ae4596e0a232ef5b7f3400f38cbc30737bd8fb"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 62, refused: 120, digest: "b786155d2bf108032d94d770109118bfe171594aca51cee135a06e55d2e22fb0"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 77, refused: 129, digest: "32734ca02fc08ac41703a387052a983b9766c573e2d56e4e2025b94cd6f4df70"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 77, refused: 129, digest: "59e8d622a3b4b2d2a948fb6f70320257366ead2bc4beee5e4c3e1dbe500a4b7d"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 77, refused: 129, digest: "932182aed12dc476ddfdec2d7f254031dff75f6c1bbf50b2501922c428cf2465"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 77, refused: 129, digest: "66a8512f95dea4d6b0b8e34eddd37fc9e15b2e607b2420b9530cdebe057140f5"},
			"refusal":    {read: 201, refused: 374, digest: "32dd3a349ea0553756a74bb9ca53774d1d75f3be1cc11adc7ba92d9dc052c0b9"},
			"silent-leg": {read: 125, refused: 220, digest: "607d1f83b02999817b8d7365b55dad5f330b8936832550d29cffa88a50e9776b"},
			"silent-leg with its matrix dropped at m2": {read: 125, refused: 220, digest: "2ac061840e74f307ce7935d26d1eaa2b083d793a72be22757014042fc297466c"},
			"wait-cycle": {read: 149, refused: 201, digest: "8403b65bed42e311d43f2297a4bb75271f32955622ce489667a45a0daad5ad1b"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 191, refused: 247, digest: "50d6d9adc358c6b9509f3c2962d2307af5c7a7fd2220d278a2ac2e11f0b47155"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 149, refused: 201, digest: "c877933d3a272b4999a5dcd71db4cafc0e6086d57de2d14ea91dc228330319ec"},
		},
		"attributes": {
			"carry": {read: 10, refused: 16, digest: "85c0d2af3eeb7d9404d5f9cd3efa619285f2a437179674ee9adca70f37aa9e8d"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 10, refused: 16, digest: "29fc0a7447298756902a6b343da21c8771bd890b3ccf047c36a1ce7e729771cf"},
			"carry with a later failing example planted by no step":                      {read: 10, refused: 16, digest: "16461cc8f6a200576cb2e96bde4404fb6ebb76ca05055375d83d536beab87483"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 10, refused: 16, digest: "4c04ebf800c7c5e26a0c8d9ac30c0b51f4051160ad4a708bc002c874fdd4aa5b"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 10, refused: 16, digest: "c246aaea505682a5904adf41f16bfddb4b6c6c5013a66890c3987f5e0b35ca5e"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 10, refused: 16, digest: "0571576f3af1f144ce8966a46181494fbac4abc90944c61835e5eb84e93e858e"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 10, refused: 16, digest: "75e10f771da7c28a84c8756f3eb8cc7ed89a2ad3753f841775df322e2d3e93a9"},
			"carry with its failing example planted by no step":                          {read: 10, refused: 16, digest: "8a0a94105c3d804da659c699312124a2ce5e19885ed1feef9562beb3f893cd1f"},
			"earlier-text":           {read: 10, refused: 12, digest: "dde79a6ce0b0d340282b81615e236caf1789693bee66fc83205415fc109255fd"},
			"one-commit-two-answers": {read: 35, refused: 33, digest: "813b94dbcdf5e16b10f6e204499aa1774c883dd3e211e6219fdaeb195424f3f7"},
			"production-and-graph":   {read: 8, refused: 16, digest: "1e056c6320ab1a41b8f6534dc792d2f6489b76aef1cf3b3c6c6dc8870e43157f"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 8, refused: 16, digest: "ad2f4848811af859a8714eadfe9b2c9fa5b4f4711ff5cc9012d18bf306b5744e"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 8, refused: 16, digest: "19711c48e7f9e2308b36851b61dbb8350e971d16b2e15315f1a12f6bf00155b1"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 8, refused: 16, digest: "21d94627d66e6a85c609eb82a735ddac90bd57d9f3d1f4594d7c7a7fa5c7a700"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 8, refused: 16, digest: "3cfab23ae0ed0adaeb9f6b9fc65a6c91e3b312a4d7ceff65ed57c52fe09ecb6f"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 8, refused: 16, digest: "dd3354d98276170ae202cf0a9aed80b72ebaf7b2962b87bdc8ab16112f06165a"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 5, refused: 13, digest: "d3ccef197ca1e87bd6215c209476e769810af575461df3352df878cda4f4da5a"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 8, refused: 16, digest: "19bd527ea218ceec7e7e7abef445a4be4cedc4b282c584184266a76b866872af"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 8, refused: 16, digest: "431f3983cd40d384f96c08f7740f8584b813a63b4a151650bbb1e3ba56736844"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 8, refused: 16, digest: "d365e52f7090b79cdf2921c63dfda9aa2abaa1e209303189c0b7dad6d89c9bb5"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 8, refused: 16, digest: "644ad0b723fa9476be041707507af625e6e0bb024ec1ddd0cdd00812f803052a"},
			"refusal":    {read: 10, refused: 16, digest: "2902dbe9ece3c49695d35fc387874e010dbc0d528d1961ead2a75b188ba6d5a3"},
			"silent-leg": {read: 10, refused: 16, digest: "4e9edc3cb70aeb6b0a6c5570cacf4936aa56cf08f2ac9c4c6ab8de11dd3c17a8"},
			"silent-leg with its matrix dropped at m2": {read: 10, refused: 16, digest: "e2b5e8f199fb9dc414b17f72398fff83c4b71f9bcb78f4bbd9d9caf6fae757b1"},
			"wait-cycle": {read: 22, refused: 24, digest: "f5b605e955afeac202114ec9a2c2488409165daa316c268064cf5d75e476787b"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 27, refused: 31, digest: "40a8d99dff762d499b25c44f0a0de3995a0ddd621d734c7221fcc5fa0f8dbf42"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 22, refused: 24, digest: "340e5082a22e3618a7b2db75deefd240fef04a37a6f98fe31b0e1d1eaaf9f0c3"},
		},
		"refusals": {
			"carry": {read: 14, refused: 0, digest: "9cbded24d70c728322093d6af2c9bb9a0f7c5242123742a81b5920bda752b304"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 18, refused: 0, digest: "6310375d88774869086cbb9aa5f653d81020c44f348981efffff0c15a93d1d8d"},
			"carry with a later failing example planted by no step":                      {read: 16, refused: 0, digest: "c76e130a80d0763a72c049eeae7264f8d60d5051c8993833c9fdef638284cdc0"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 16, refused: 0, digest: "93def328835446bef0f3236be113030c19723595e8cd08fd3cdc8790137d17be"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 18, refused: 0, digest: "96fc815fac3bf82a9e6acbb827a58436ae1a44bc4369ec17065d696cc576af09"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 16, refused: 0, digest: "80ec4aa988b91daa47e1359ea7c8c7dd99d0c0a231a6ced8278e4d942e3ed781"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 16, refused: 0, digest: "13b262370feac243626c65f87d9624b0d433434fb516681f6822485f2cc902d9"},
			"carry with its failing example planted by no step":                          {read: 12, refused: 0, digest: "ada2ff13b7a26a89b8ef47e5f4ce5cb503a9eee27f7dd9bf0a6cfc48186a8300"},
			"earlier-text":           {read: 5, refused: 0, digest: "42d31e751dc899b8a72c0b3fed5f06ddbb4e5ff94ee6b44b72e95e62bc825dbb"},
			"one-commit-two-answers": {read: 20, refused: 0, digest: "de5f8937a7bf78a8dde7f9549fd8ad4f41b0ccd49dc79107e3693cc13e182438"},
			"production-and-graph":   {read: 4, refused: 0, digest: "166e785185ae6a81962bce878d754df1a7a54afe6e33e5b3bbfafc80c9f4a6a1"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 3, refused: 0, digest: "f4d473a1d0f4a58bdfefb3d2b1845acb21b0d033fc68db4571a5f575e3fefef4"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 3, refused: 0, digest: "61c3a14addcfe5e19c22588c8c15ac6ebbf2b25074834c0e1d2e55ff39744e78"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 3, refused: 0, digest: "908f2644976b9e8b8b49e3b060b573137a44f93c148161124bd591fec4602a2f"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 3, refused: 0, digest: "8dd2a6cf0049b6416328a8c1a3866528d7cbf9fb54da9fd6f0feb1fa8e89f5d5"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 4, refused: 0, digest: "46b0968e12a8fa5ad76f77129ea7ec316ef4e58698e844386af3d2628bc33b8f"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 4, refused: 0, digest: "f3430bbcccb6d9c617e1f7c8d1fa69000b10426de646e095b01767b92284cdda"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 4, refused: 0, digest: "003d9bf8d334645ad1368aa232a6894dbb20f160350cb649edd2689415aee2b7"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 4, refused: 0, digest: "d6ad815b918a08d8c6bc6c48bf07679ee07447d5513382ab3dec0851595b9d52"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 4, refused: 0, digest: "3705c674febc03666faec7b76c3a9a1bbf282f86c8400574c013b967d8180cdc"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 4, refused: 0, digest: "5e2669f419940cf3dd49047aa5ac7f3feeeb94a2d3fb40fb5868fa5be353681a"},
			"refusal":    {read: 13, refused: 0, digest: "160faa3b27db8218f6e768076fcc9f3e24d0224ef23ef90618ee08e2b7ef4725"},
			"silent-leg": {read: 10, refused: 0, digest: "2cb03615958afed9f3ee23943b2fc413c752bb6a78faf1f96b5b4719a6369617"},
			"silent-leg with its matrix dropped at m2": {read: 10, refused: 0, digest: "4bc1dfe318750672e28af48d9d1fbb9eff7f28488c4e76d70ffcae7e6a06e47c"},
			"wait-cycle": {read: 18, refused: 0, digest: "63443c5cf4df694bcd26a7ee83ae64f120cbeae9858cf01abd3f7dd2fc9756f4"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 24, refused: 0, digest: "b37580b9f98cd044249dd4fbace9c8ae0702891fd020fa43cd70ac8442104f97"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 18, refused: 0, digest: "e3a694b3f3a02a4c46d8eee97e1146491994a1b063c1c7c8ee8838a9c18eac3c"},
		},
	}
}

// sweepFloor is the floor of one sweep of one base: the inputs that the reader read, as the base or
// as another state, and the inputs that it refused, as sweepAgainst counted the inputs that it
// judged (sweepCount), and digest, the hex sha256 of sweepCount.judged: the key of the base's line
// (readerLine, the sha256 of the bytes that the reader's process read of the base), then each judged
// input's key and kind, one byte, 'r' read and 'x' refused, in their order.
type sweepFloor struct {
	read, refused int
	digest        string
}

// markReadingsFloor is the count of readings of a member left out that
// TestAMemberLeftOutOfAFileIsReadByItsMark read when the floor was written.
const markReadingsFloor = 82

// atFloor fails the test unless, of the inputs of the base name that sweepAgainst judged (n), the
// reader read at least the floor's count and refused at least the floor's count, and the base's
// files and the files and kinds of those inputs, in their order, have the floor's digest
// (sweepFloors). It counts no input that the sweep built and did not judge.
func atFloor(t *testing.T, sweep, name string, n sweepCount) {
	t.Helper()
	floor, held := sweepFloors()[sweep][name]
	if read := n.same + n.moved; !held || read < floor.read || n.refused < floor.refused {
		t.Errorf("the %s sweep judged %d inputs of %s that the reader read and %d that it refused, and its floor is %d read and %d refused (held %t; sweepFloors)",
			sweep, read, name, n.refused, floor.read, floor.refused, held)
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256(n.judged)); held && digest != floor.digest {
		t.Errorf("the %s sweep judged the %d inputs of %s, in their order, as files and kinds whose digest is %s, and its floor's digest is %s (sweepFloors): the base's files or an input's files changed, an input moved from one kind to the other, or the sweep judged other inputs",
			sweep, n.same+n.moved+n.refused, name, digest, floor.digest)
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
