export namespace main {
	
	export class AddLocalModelInput {
	    directory: string;
	    id: string;
	
	    static createFrom(source: any = {}) {
	        return new AddLocalModelInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.directory = source["directory"];
	        this.id = source["id"];
	    }
	}
	export class AddRepoModelInput {
	    repo: string;
	    id: string;
	    engine: string;
	
	    static createFrom(source: any = {}) {
	        return new AddRepoModelInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.repo = source["repo"];
	        this.id = source["id"];
	        this.engine = source["engine"];
	    }
	}
	export class BurnSubtitlesInput {
	    outputPath: string;
	    fontName: string;
	    fontSize: number;
	    marginV: number;
	
	    static createFrom(source: any = {}) {
	        return new BurnSubtitlesInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.outputPath = source["outputPath"];
	        this.fontName = source["fontName"];
	        this.fontSize = source["fontSize"];
	        this.marginV = source["marginV"];
	    }
	}
	export class CreateTaskInput {
	    sourceFile: string;
	    sourceLanguage: string;
	    targetLanguage: string;
	    outputFormat: string;
	    engine: string;
	    model: string;
	    translate: boolean;
	    provider: string;
	
	    static createFrom(source: any = {}) {
	        return new CreateTaskInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sourceFile = source["sourceFile"];
	        this.sourceLanguage = source["sourceLanguage"];
	        this.targetLanguage = source["targetLanguage"];
	        this.outputFormat = source["outputFormat"];
	        this.engine = source["engine"];
	        this.model = source["model"];
	        this.translate = source["translate"];
	        this.provider = source["provider"];
	    }
	}
	export class Task {
	    id: string;
	    name: string;
	    sourceFile: string;
	    sourceLanguage: string;
	    targetLanguage: string;
	    status: string;
	    stage: string;
	    progress: number;
	    currentStage: string;
	    subtitleCount: number;
	    translatedCount: number;
	    message: string;
	    error: string;
	    engine: string;
	    model: string;
	    provider: string;
	    outputFormat: string;
	    duration: number;
	    createdAt: string;
	    updatedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new Task(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.sourceFile = source["sourceFile"];
	        this.sourceLanguage = source["sourceLanguage"];
	        this.targetLanguage = source["targetLanguage"];
	        this.status = source["status"];
	        this.stage = source["stage"];
	        this.progress = source["progress"];
	        this.currentStage = source["currentStage"];
	        this.subtitleCount = source["subtitleCount"];
	        this.translatedCount = source["translatedCount"];
	        this.message = source["message"];
	        this.error = source["error"];
	        this.engine = source["engine"];
	        this.model = source["model"];
	        this.provider = source["provider"];
	        this.outputFormat = source["outputFormat"];
	        this.duration = source["duration"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	    }
	}
	export class Dashboard {
	    activeTask?: Task;
	    tasks: Task[];
	
	    static createFrom(source: any = {}) {
	        return new Dashboard(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.activeTask = this.convertValues(source["activeTask"], Task);
	        this.tasks = this.convertValues(source["tasks"], Task);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DownloadFFmpegInput {
	    dir: string;
	    sourceId: string;
	
	    static createFrom(source: any = {}) {
	        return new DownloadFFmpegInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dir = source["dir"];
	        this.sourceId = source["sourceId"];
	    }
	}
	export class EnvironmentStatus {
	    ffmpegPath: string;
	    ffmpegOk: boolean;
	    ffmpegDetail: string;
	    ffmpegSubtitle: boolean;
	    ffmpegDefaultDir: string;
	    pythonPath: string;
	    pythonOk: boolean;
	    pythonDetail: string;
	    fasterWhisper: boolean;
	    transformers: boolean;
	    transformersDetail: string;
	    gpu: string;
	    modelDir: string;
	
	    static createFrom(source: any = {}) {
	        return new EnvironmentStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ffmpegPath = source["ffmpegPath"];
	        this.ffmpegOk = source["ffmpegOk"];
	        this.ffmpegDetail = source["ffmpegDetail"];
	        this.ffmpegSubtitle = source["ffmpegSubtitle"];
	        this.ffmpegDefaultDir = source["ffmpegDefaultDir"];
	        this.pythonPath = source["pythonPath"];
	        this.pythonOk = source["pythonOk"];
	        this.pythonDetail = source["pythonDetail"];
	        this.fasterWhisper = source["fasterWhisper"];
	        this.transformers = source["transformers"];
	        this.transformersDetail = source["transformersDetail"];
	        this.gpu = source["gpu"];
	        this.modelDir = source["modelDir"];
	    }
	}
	export class FFmpegSource {
	    id: string;
	    label: string;
	    url: string;
	    note: string;
	    license: string;
	
	    static createFrom(source: any = {}) {
	        return new FFmpegSource(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.url = source["url"];
	        this.note = source["note"];
	        this.license = source["license"];
	    }
	}
	export class FFmpegStatus {
	    configured: boolean;
	    path: string;
	    version: string;
	    ok: boolean;
	    canBurn: boolean;
	    detail: string;
	    defaultDir: string;
	    dataDir: string;
	    downloading: boolean;
	    sources: FFmpegSource[];
	
	    static createFrom(source: any = {}) {
	        return new FFmpegStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.configured = source["configured"];
	        this.path = source["path"];
	        this.version = source["version"];
	        this.ok = source["ok"];
	        this.canBurn = source["canBurn"];
	        this.detail = source["detail"];
	        this.defaultDir = source["defaultDir"];
	        this.dataDir = source["dataDir"];
	        this.downloading = source["downloading"];
	        this.sources = this.convertValues(source["sources"], FFmpegSource);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ModelInfo {
	    id: string;
	    name: string;
	    repo: string;
	    sizeBytes: number;
	    installed: boolean;
	    installing: boolean;
	    progress: number;
	    localPath: string;
	    mirror: string;
	    description: string;
	    files: string[];
	    engine: string;
	    custom: boolean;
	    gated: boolean;
	    local: boolean;
	    installedOk: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ModelInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.repo = source["repo"];
	        this.sizeBytes = source["sizeBytes"];
	        this.installed = source["installed"];
	        this.installing = source["installing"];
	        this.progress = source["progress"];
	        this.localPath = source["localPath"];
	        this.mirror = source["mirror"];
	        this.description = source["description"];
	        this.files = source["files"];
	        this.engine = source["engine"];
	        this.custom = source["custom"];
	        this.gated = source["gated"];
	        this.local = source["local"];
	        this.installedOk = source["installedOk"];
	    }
	}
	export class OllamaModelInfo {
	    name: string;
	    size: number;
	    parameterSize: string;
	    quantization: string;
	    family: string;
	    modifiedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new OllamaModelInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.size = source["size"];
	        this.parameterSize = source["parameterSize"];
	        this.quantization = source["quantization"];
	        this.family = source["family"];
	        this.modifiedAt = source["modifiedAt"];
	    }
	}
	export class SettingsPatch {
	    ffmpegPath?: string;
	    pythonPath?: string;
	    modelDir?: string;
	    mirror?: string;
	    huggingFaceEndpoint?: string;
	    asrEngine?: string;
	    asrDevice?: string;
	    computeType?: string;
	    defaultModel?: string;
	    translationProvider?: string;
	    translationEndpoint?: string;
	    translationModel?: string;
	    translationApiKey?: string;
	    cloudPrivacyConfirm?: boolean;
	    translationStyle?: string;
	    glossaryText?: string;
	    huggingFaceToken?: string;
	    noiseReduction?: string;
	    noiseReductionFilter?: string;
	    noiseReductionSampleRate?: number;
	    beamSize?: number;
	    vadFilter?: boolean;
	    vadThreshold?: number;
	    vadMinSilenceMs?: number;
	    vadPaddingMs?: number;
	    hallucinationFilter?: string;
	    conditionOnPreviousText?: boolean;
	    noSpeechThreshold?: number;
	    compressionRatioThreshold?: number;
	
	    static createFrom(source: any = {}) {
	        return new SettingsPatch(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ffmpegPath = source["ffmpegPath"];
	        this.pythonPath = source["pythonPath"];
	        this.modelDir = source["modelDir"];
	        this.mirror = source["mirror"];
	        this.huggingFaceEndpoint = source["huggingFaceEndpoint"];
	        this.asrEngine = source["asrEngine"];
	        this.asrDevice = source["asrDevice"];
	        this.computeType = source["computeType"];
	        this.defaultModel = source["defaultModel"];
	        this.translationProvider = source["translationProvider"];
	        this.translationEndpoint = source["translationEndpoint"];
	        this.translationModel = source["translationModel"];
	        this.translationApiKey = source["translationApiKey"];
	        this.cloudPrivacyConfirm = source["cloudPrivacyConfirm"];
	        this.translationStyle = source["translationStyle"];
	        this.glossaryText = source["glossaryText"];
	        this.huggingFaceToken = source["huggingFaceToken"];
	        this.noiseReduction = source["noiseReduction"];
	        this.noiseReductionFilter = source["noiseReductionFilter"];
	        this.noiseReductionSampleRate = source["noiseReductionSampleRate"];
	        this.beamSize = source["beamSize"];
	        this.vadFilter = source["vadFilter"];
	        this.vadThreshold = source["vadThreshold"];
	        this.vadMinSilenceMs = source["vadMinSilenceMs"];
	        this.vadPaddingMs = source["vadPaddingMs"];
	        this.hallucinationFilter = source["hallucinationFilter"];
	        this.conditionOnPreviousText = source["conditionOnPreviousText"];
	        this.noSpeechThreshold = source["noSpeechThreshold"];
	        this.compressionRatioThreshold = source["compressionRatioThreshold"];
	    }
	}
	export class SettingsSnapshot {
	    ffmpegPath: string;
	    pythonPath: string;
	    modelDir: string;
	    mirror: string;
	    huggingFaceEndpoint: string;
	    asrEngine: string;
	    asrDevice: string;
	    computeType: string;
	    defaultModel: string;
	    translationProvider: string;
	    translationEndpoint: string;
	    translationModel: string;
	    translationApiKeySet: boolean;
	    cloudPrivacyConfirm: boolean;
	    translationStyle: string;
	    glossaryText: string;
	    huggingFaceTokenSet: boolean;
	    localModels: Record<string, string>;
	    noiseReduction: string;
	    noiseReductionFilter: string;
	    noiseReductionSampleRate: number;
	    beamSize: number;
	    vadFilter: boolean;
	    vadThreshold: number;
	    vadMinSilenceMs: number;
	    vadPaddingMs: number;
	    hallucinationFilter: string;
	    conditionOnPreviousText: boolean;
	    noSpeechThreshold: number;
	    compressionRatioThreshold: number;
	
	    static createFrom(source: any = {}) {
	        return new SettingsSnapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ffmpegPath = source["ffmpegPath"];
	        this.pythonPath = source["pythonPath"];
	        this.modelDir = source["modelDir"];
	        this.mirror = source["mirror"];
	        this.huggingFaceEndpoint = source["huggingFaceEndpoint"];
	        this.asrEngine = source["asrEngine"];
	        this.asrDevice = source["asrDevice"];
	        this.computeType = source["computeType"];
	        this.defaultModel = source["defaultModel"];
	        this.translationProvider = source["translationProvider"];
	        this.translationEndpoint = source["translationEndpoint"];
	        this.translationModel = source["translationModel"];
	        this.translationApiKeySet = source["translationApiKeySet"];
	        this.cloudPrivacyConfirm = source["cloudPrivacyConfirm"];
	        this.translationStyle = source["translationStyle"];
	        this.glossaryText = source["glossaryText"];
	        this.huggingFaceTokenSet = source["huggingFaceTokenSet"];
	        this.localModels = source["localModels"];
	        this.noiseReduction = source["noiseReduction"];
	        this.noiseReductionFilter = source["noiseReductionFilter"];
	        this.noiseReductionSampleRate = source["noiseReductionSampleRate"];
	        this.beamSize = source["beamSize"];
	        this.vadFilter = source["vadFilter"];
	        this.vadThreshold = source["vadThreshold"];
	        this.vadMinSilenceMs = source["vadMinSilenceMs"];
	        this.vadPaddingMs = source["vadPaddingMs"];
	        this.hallucinationFilter = source["hallucinationFilter"];
	        this.conditionOnPreviousText = source["conditionOnPreviousText"];
	        this.noSpeechThreshold = source["noSpeechThreshold"];
	        this.compressionRatioThreshold = source["compressionRatioThreshold"];
	    }
	}
	export class SubtitleSegment {
	    id: string;
	    sequence: number;
	    start: number;
	    end: number;
	    sourceText: string;
	    translatedText: string;
	    translationStatus: string;
	
	    static createFrom(source: any = {}) {
	        return new SubtitleSegment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.sequence = source["sequence"];
	        this.start = source["start"];
	        this.end = source["end"];
	        this.sourceText = source["sourceText"];
	        this.translatedText = source["translatedText"];
	        this.translationStatus = source["translationStatus"];
	    }
	}

}

