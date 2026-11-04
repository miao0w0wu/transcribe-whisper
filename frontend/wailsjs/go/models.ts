export namespace main {
	
	export class CreateTaskInput {
	    sourceFile: string;
	    sourceLanguage: string;
	    targetLanguage: string;
	    outputFormat: string;
	
	    static createFrom(source: any = {}) {
	        return new CreateTaskInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sourceFile = source["sourceFile"];
	        this.sourceLanguage = source["sourceLanguage"];
	        this.targetLanguage = source["targetLanguage"];
	        this.outputFormat = source["outputFormat"];
	    }
	}
	export class Task {
	    id: string;
	    name: string;
	    sourceFile: string;
	    sourceLanguage: string;
	    targetLanguage: string;
	    status: string;
	    progress: number;
	    currentStage: string;
	    subtitleCount: number;
	    createdAt: string;
	
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
	        this.progress = source["progress"];
	        this.currentStage = source["currentStage"];
	        this.subtitleCount = source["subtitleCount"];
	        this.createdAt = source["createdAt"];
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

