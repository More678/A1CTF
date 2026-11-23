import { useRef, useState } from "react";
import { useFieldArray, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "react-toastify/unstyled";
import { FileArchive, Files, ListChecks, PlusCircle, RefreshCcw, Trash2 } from "lucide-react";

import { Button } from "components/ui/button";
import { Input } from "components/ui/input";
import { Badge } from "components/ui/badge";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "components/ui/dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "components/ui/table";
import { ConfirmDialog, DialogOption } from "components/dialogs/ConfirmDialog";
import { AdminChallengeConfig, AttachmentPoolAssignment } from "utils/A1API";
import { api, createSkipGlobalErrorConfig } from "utils/ApiHelper";

interface AttachmentPoolEditorProps {
    control: any;
    form: any;
    index: number;
    /** 已保存的题目才有，用于重新分配和查看分配 */
    challengeId?: number;
}

/**
 * 动态Flag（附件池）的条目编辑器：导入 zip / 多个文件、编辑每个条目的 flag、重新分配、查看各队伍的分配。
 */
export function AttachmentPoolEditor({ control, form, index, challengeId }: AttachmentPoolEditorProps) {
    const { t } = useTranslation("challenge_edit");
    const name = `attachments.${index}.pool`;

    const { fields, append, remove } = useFieldArray({ control, name });
    const entries: { file_id: string; flag: string }[] = useWatch({ control, name }) ?? [];

    // 导入时的原始文件名，仅用于展示，不保存
    const [originalNames, setOriginalNames] = useState<Record<string, string>>({});
    const [importing, setImporting] = useState(false);
    const archiveInput = useRef<HTMLInputElement>(null);
    const filesInput = useRef<HTMLInputElement>(null);

    const [assignments, setAssignments] = useState<AttachmentPoolAssignment[] | null>(null);
    const [dialogOption, setDialogOption] = useState<DialogOption>({ isOpen: false, message: "" });

    const doImport = (field: "archive" | "files", fileList: FileList | null) => {
        if (!fileList || fileList.length === 0) return;
        const data = new FormData();
        Array.from(fileList).forEach((f) => data.append(field, f));

        setImporting(true);
        api.admin.importAttachmentPool(data).then((res) => {
            const imported = res.data.data;
            imported.forEach((e) => append({ file_id: e.file_id, flag: e.flag }));
            setOriginalNames((prev) => {
                const next = { ...prev };
                imported.forEach((e) => { next[e.file_id] = e.file_name; });
                return next;
            });
            form.trigger(name);
            toast.success(t("attachment.pool.import_success", { count: imported.length }));
        }).finally(() => {
            setImporting(false);
            if (archiveInput.current) archiveInput.current.value = "";
            if (filesInput.current) filesInput.current.value = "";
        });
    };

    const reassign = () => {
        if (!challengeId) return;
        setDialogOption({
            isOpen: true,
            title: t("attachment.pool.reassign"),
            message: t("attachment.pool.reassign_confirm"),
            onConfirm: () => {
                api.admin.reassignAttachmentPool(challengeId).then((res) => {
                    toast.success(t("attachment.pool.reassign_success", { count: res.data.data.changed_teams }));
                });
            },
        });
    };

    const showAssignments = () => {
        if (!challengeId) return;
        api.admin.getAttachmentPoolAssignments(challengeId).then((res) => {
            setAssignments(res.data.data);
        });
    };

    return (
        <div className="mt-4 flex flex-col gap-3">
            <ConfirmDialog settings={dialogOption} setSettings={setDialogOption} />

            <div className="text-sm text-muted-foreground whitespace-pre-line">{t("attachment.pool.description")}</div>

            <div className="flex flex-wrap gap-2">
                <input ref={archiveInput} type="file" accept=".zip" className="hidden"
                    onChange={(e) => doImport("archive", e.target.files)} />
                <input ref={filesInput} type="file" multiple className="hidden"
                    onChange={(e) => doImport("files", e.target.files)} />
                <Button type="button" variant="outline" disabled={importing} onClick={() => archiveInput.current?.click()}>
                    <FileArchive />
                    {t("attachment.pool.import_archive")}
                </Button>
                <Button type="button" variant="outline" disabled={importing} onClick={() => filesInput.current?.click()}>
                    <Files />
                    {t("attachment.pool.import_files")}
                </Button>
                <Button type="button" variant="outline" onClick={() => append({ file_id: "", flag: "" })}>
                    <PlusCircle />
                    {t("attachment.pool.add_entry")}
                </Button>
                <div className="flex-1" />
                {challengeId ? (
                    <>
                        <Button type="button" variant="outline" onClick={showAssignments}>
                            <ListChecks />
                            {t("attachment.pool.assignments")}
                        </Button>
                        <Button type="button" variant="outline" onClick={reassign}>
                            <RefreshCcw />
                            {t("attachment.pool.reassign")}
                        </Button>
                    </>
                ) : null}
            </div>

            <span className="text-sm">{t("attachment.pool.count", { count: fields.length })}</span>
            {form.formState.errors?.attachments?.[index]?.pool?.message && (
                <span className="text-sm text-destructive">{form.formState.errors.attachments[index].pool.message}</span>
            )}

            {fields.length > 0 && (
                <div className="rounded-lg border max-h-[420px] overflow-auto">
                    <Table>
                        <TableHeader>
                            <TableRow>
                                <TableHead className="w-[60px]">#</TableHead>
                                <TableHead>{t("attachment.pool.file_id")}</TableHead>
                                <TableHead>Flag</TableHead>
                                <TableHead className="w-[60px]" />
                            </TableRow>
                        </TableHeader>
                        <TableBody>
                            {fields.map((field, i) => (
                                <TableRow key={field.id}>
                                    <TableCell>{i + 1}</TableCell>
                                    <TableCell>
                                        <Input {...form.register(`${name}.${i}.file_id`)} className="font-mono text-xs"
                                            placeholder={t("attachment.pool.file_id")} />
                                        {originalNames[entries[i]?.file_id] && (
                                            <span className="text-xs text-muted-foreground">{originalNames[entries[i].file_id]}</span>
                                        )}
                                    </TableCell>
                                    <TableCell>
                                        <Input {...form.register(`${name}.${i}.flag`)} className="font-mono text-xs" />
                                    </TableCell>
                                    <TableCell>
                                        <Button type="button" size="icon" variant="ghost" onClick={() => remove(i)}>
                                            <Trash2 />
                                        </Button>
                                    </TableCell>
                                </TableRow>
                            ))}
                        </TableBody>
                    </Table>
                </div>
            )}

            <Dialog open={assignments !== null} onOpenChange={(open) => { if (!open) setAssignments(null); }}>
                <DialogContent className="sm:max-w-[900px] select-none">
                    <DialogHeader>
                        <DialogTitle>{t("attachment.pool.assignments")}</DialogTitle>
                    </DialogHeader>
                    {assignments && assignments.length > 0 ? (
                        <div className="max-h-[60vh] overflow-auto">
                            <Table>
                                <TableHeader>
                                    <TableRow>
                                        <TableHead>{t("attachment.pool.game")}</TableHead>
                                        <TableHead>{t("attachment.pool.team")}</TableHead>
                                        <TableHead>#</TableHead>
                                        <TableHead>Flag</TableHead>
                                        <TableHead>{t("attachment.pool.status")}</TableHead>
                                    </TableRow>
                                </TableHeader>
                                <TableBody>
                                    {assignments.map((a) => (
                                        <TableRow key={`${a.game_id}-${a.team_id}`}>
                                            <TableCell>{a.game_name}</TableCell>
                                            <TableCell>{a.team_name}</TableCell>
                                            <TableCell>
                                                {a.entry_index >= 0
                                                    ? a.entry_index + 1
                                                    : <Badge variant="destructive">{t("attachment.pool.entry_removed")}</Badge>}
                                            </TableCell>
                                            <TableCell className="font-mono text-xs">{a.flag}</TableCell>
                                            <TableCell>
                                                {a.solved ? <Badge>{t("attachment.pool.solved")}</Badge> : <Badge variant="secondary">{t("attachment.pool.unsolved")}</Badge>}
                                            </TableCell>
                                        </TableRow>
                                    ))}
                                </TableBody>
                            </Table>
                        </div>
                    ) : (
                        <span className="text-sm text-muted-foreground">{t("attachment.pool.no_assignments")}</span>
                    )}
                </DialogContent>
            </Dialog>
        </div>
    );
}

/**
 * 保存题目。如果后端因「已分配的附件池条目被删除或修改」返回 409，弹窗确认后带 confirm_reassign=true 重试。
 */
export function usePoolAwareChallengeUpdate() {
    const { t } = useTranslation("challenge_edit");
    const [dialogOption, setDialogOption] = useState<DialogOption>({ isOpen: false, message: "" });

    const update = (challengeId: number, data: AdminChallengeConfig, onSuccess: () => void) => {
        api.admin.updateChallenge(challengeId, data, undefined, createSkipGlobalErrorConfig()).then(onSuccess).catch((error: any) => {
            const response = error?.response;
            const message = response?.data?.message ?? t("attachment.pool.save_failed");
            if (response?.status === 409 && response?.data?.affected_teams !== undefined) {
                setDialogOption({
                    isOpen: true,
                    title: t("attachment.pool.reassign"),
                    message,
                    onConfirm: () => {
                        api.admin.updateChallenge(challengeId, data, { confirm_reassign: true }).then(onSuccess);
                    },
                });
                return;
            }
            toast.error(message);
        });
    };

    const dialog = <ConfirmDialog settings={dialogOption} setSettings={setDialogOption} />;

    return { update, dialog };
}

export const POOL_DEFAULT_ATTACHMENT = {
    attach_hash: null,
    attach_name: "attachment.zip",
    attach_type: "ATTACHMENTPOOR" as const,
    attach_url: "",
    download_hash: "",
    generate_script: "",
    pool: [] as { file_id: string; flag: string }[],
};

const POOL_FLAG_TYPE = "FlagTypeAttachmentPool";
const POOL_ATTACH_TYPE = "ATTACHMENTPOOR";

type PoolFormAttachment = { attach_type: string; pool?: { file_id: string; flag: string }[] };

/** 附件类型切换时联动 flag 类型：选中附件池 => 动态Flag（附件池）；离开附件池 => 静态 Flag */
export function onAttachTypeChange(form: any, index: number, value: string) {
    if (value === POOL_ATTACH_TYPE) {
        if (!form.getValues(`attachments.${index}.pool`)) {
            form.setValue(`attachments.${index}.pool`, []);
        }
        form.setValue("flag_type", POOL_FLAG_TYPE);
        form.setValue("judge_config.judge_type", "DYNAMIC");
        return;
    }
    if (form.getValues("flag_type") === POOL_FLAG_TYPE) {
        const attachments: PoolFormAttachment[] = form.getValues("attachments") ?? [];
        const otherPool = attachments.some((a, i) => i !== index && a.attach_type === POOL_ATTACH_TYPE);
        if (!otherPool) form.setValue("flag_type", "FlagTypeStatic");
    }
}

/** flag 类型切换时联动附件：选中动态Flag（附件池）=> 附件只保留一个附件池；离开 => 移除附件池 */
export function onFlagTypeChange(form: any, value: string) {
    const attachments: PoolFormAttachment[] = form.getValues("attachments") ?? [];
    const pools = attachments.filter((a) => a.attach_type === POOL_ATTACH_TYPE);
    if (value === POOL_FLAG_TYPE) {
        form.setValue("judge_config.judge_type", "DYNAMIC");
        if (pools.length !== 1 || attachments.length !== 1) {
            form.setValue("attachments", [pools[0] ?? { ...POOL_DEFAULT_ATTACHMENT, pool: [] }]);
        }
    } else if (pools.length > 0) {
        form.setValue("attachments", attachments.filter((a) => a.attach_type !== POOL_ATTACH_TYPE));
    }
}

/** zod superRefine：与后端 attachmentpool.Validate 保持一致 */
export function refinePoolForm(
    values: { flag_type: string; attachments: PoolFormAttachment[] },
    ctx: { addIssue: (issue: any) => void },
    t: (key: string) => string,
) {
    const isPoolFlag = values.flag_type === POOL_FLAG_TYPE;
    const poolIndexes = values.attachments
        .map((a, i) => (a.attach_type === POOL_ATTACH_TYPE ? i : -1))
        .filter((i) => i >= 0);

    if (isPoolFlag && poolIndexes.length === 0) {
        ctx.addIssue({ code: "custom", path: ["flag_type"], message: t("attachment.pool.error.need_pool") });
    }
    poolIndexes.forEach((i) => {
        if (!isPoolFlag) {
            ctx.addIssue({ code: "custom", path: ["attachments", i, "attach_type"], message: t("attachment.pool.error.need_flag_type") });
        }
        if (values.attachments.length !== 1) {
            ctx.addIssue({ code: "custom", path: ["attachments", i, "attach_type"], message: t("attachment.pool.error.only_one") });
        }
        if (!values.attachments[i].pool || values.attachments[i].pool!.length === 0) {
            ctx.addIssue({ code: "custom", path: ["attachments", i, "pool"], message: t("attachment.pool.error.empty") });
        }
    });
}
