/*
 * Tencent is pleased to support the open source community by making 蓝鲸 available.
 * Copyright (C) 2017-2018 THL A29 Limited, a Tencent company. All rights reserved.
 * Licensed under the MIT License (the "License"); you may not use this file except
 * in compliance with the License. You may obtain a copy of the License at
 * http://opensource.org/licenses/MIT
 * Unless required by applicable law or agreed to in writing, software distributed under
 * the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
 * either express or implied. See the License for the specific language governing permissions and
 * limitations under the License.
 */

package inst

import (
	"configcenter/src/apimachinery"
	"configcenter/src/common"
	"configcenter/src/common/blog"
	"configcenter/src/common/errors"
	"configcenter/src/common/http/rest"
	"configcenter/src/common/language"
	"configcenter/src/common/mapstr"
	"configcenter/src/common/metadata"
	"configcenter/src/common/util"
	"configcenter/src/common/version"
)

// SetOperationInterface set operation methods
type SetOperationInterface interface {
	CreateSet(kit *rest.Kit, bizID int64, data mapstr.MapStr) (mapstr.MapStr, error)
	DeleteSet(kit *rest.Kit, bizID int64, setIDS []int64) error
	UpdateSet(kit *rest.Kit, data mapstr.MapStr, bizID, setID int64) error
	SetProxy(inst InstOperationInterface, module ModuleOperationInterface)
}

// NewSetOperation create a set instance
func NewSetOperation(client apimachinery.ClientSetInterface, languageIf language.CCLanguageIf) SetOperationInterface {
	return &set{
		clientSet: client,
		language:  languageIf,
	}
}

type set struct {
	clientSet apimachinery.ClientSetInterface
	inst      InstOperationInterface
	module    ModuleOperationInterface
	language  language.CCLanguageIf
}

// SetProxy 初始化依赖
func (s *set) SetProxy(inst InstOperationInterface, module ModuleOperationInterface) {
	s.inst = inst
	s.module = module
}

// isSetDuplicateError check set exist
func (s *set) isSetDuplicateError(inputErr error) bool {
	ccErr, ok := inputErr.(errors.CCErrorCoder)
	if !ok {
		return false
	}

	if ccErr.GetCode() == common.CCErrCommDuplicateItem {
		return true
	}

	return false
}

// getSetTemplate get set template
func (s *set) getSetTemplate(kit *rest.Kit, data mapstr.MapStr, bizID int64) (metadata.SetTemplate, error) {
	setTemplate := metadata.SetTemplate{}
	// validate foreign key
	setTemplateIDIf, ok := data[common.BKSetTemplateIDField]
	if !ok {
		return setTemplate, nil
	}

	setTemplateID, err := util.GetInt64ByInterface(setTemplateIDIf)
	if err != nil {
		blog.Errorf("parse set_template_id field into int failed, err: %v, rid: %s", err, kit.Rid)
		err := kit.CCError.CCErrorf(common.CCErrCommParamsInvalid, s.language.CreateDefaultCCLanguageIf(util.
			GetLanguage(kit.Header)).Language("set_property_set_template_id"))
		return setTemplate, err
	}
	if setTemplateID == common.SetTemplateIDNotSet {
		return setTemplate, nil
	}

	st, err := s.clientSet.CoreService().SetTemplate().GetSetTemplate(kit.Ctx, kit.Header, bizID, setTemplateID)
	if err != nil {
		blog.Errorf("get set template failed, bizID: %d, setTemplateID: %d, err: %v, rid: %s", bizID,
			setTemplateID, kit.Rid)
		return setTemplate, err
	}

	return st, nil
}

// CreateSet create a new set
func (s *set) CreateSet(kit *rest.Kit, bizID int64, data mapstr.MapStr) (mapstr.MapStr, error) {
	data.Set(common.BKAppIDField, bizID)

	if !data.Exists(common.BKDefaultField) {
		data.Set(common.BKDefaultField, common.DefaultFlagDefaultValue)
	}
	defaultVal, err := data.Int64(common.BKDefaultField)
	if err != nil {
		blog.Errorf("parse default field into int failed, data: %#v, rid: %s", data, kit.Rid)
		return nil, err
	}

	setTemplate, err := s.getSetTemplate(kit, data, bizID)
	if err != nil {
		blog.Errorf("get set template failed, data: %#v, err: %s, rid: %s", data, err, kit.Rid)
		return nil, err
	}

	// if need create set using set template
	if setTemplate.ID == common.SetTemplateIDNotSet && !version.CanCreateSetModuleWithoutTemplate && defaultVal == 0 {
		blog.Errorf("service template not exist, can not create set, rid: %s", kit.Rid)
		return nil, kit.CCError.Errorf(common.CCErrCommParamsInvalid, "set_template_id can not be 0")
	}

	data.Set(common.BKSetTemplateIDField, setTemplate.ID)
	data.Remove(common.MetadataField)

	setInstance, err := s.inst.CreateInst(kit, common.BKInnerObjIDSet, data)
	if err != nil {
		blog.Errorf("create set instance failed, data: %#v, err: %v, rid: %s", data, err, kit.Rid)
		// return this duplicate error for unique validation failed
		if s.isSetDuplicateError(err) {
			return setInstance, kit.CCError.CCError(common.CCErrorSetNameDuplicated)
		}
		return setInstance, err
	}
	if setInstance == nil {
		blog.Errorf("create set returns nil pointer, data: %#v, rid: %s", bizID, data, kit.Rid)
		return nil, kit.CCError.CCError(common.CCErrTopoSetCreateFailed)
	}

	if setTemplate.ID == common.SetTemplateIDNotSet {
		return setInstance, nil
	}

	// set create by template should create module at the same time
	serviceTemplates, err := s.clientSet.CoreService().SetTemplate().ListSetTplRelatedSvcTpl(kit.Ctx, kit.Header,
		bizID, setTemplate.ID)
	if err != nil {
		blog.Errorf("list set tpl related svc tpl failed, bizID: %d, setTemplateID: %d, err: %v, rid: %s", bizID,
			setTemplate.ID, err, kit.Rid)
		return setInstance, err
	}

	setID, err := metadata.GetInstID(common.BKInnerObjIDSet, setInstance)
	if err != nil {
		blog.Errorf("get inst id failed, err: %v, rid: %s", err, kit.Rid)
		return setInstance, err
	}

	for _, serviceTemplate := range serviceTemplates {
		createModuleParam := mapstr.MapStr{
			common.BKModuleNameField:        serviceTemplate.Name,
			common.BKServiceTemplateIDField: serviceTemplate.ID,
			common.BKSetTemplateIDField:     setTemplate.ID,
			common.BKParentIDField:          setID,
			common.BKServiceCategoryIDField: serviceTemplate.ServiceCategoryID,
			common.BKAppIDField:             bizID,
		}

		if _, err := s.module.CreateModule(kit, bizID, setID, createModuleParam); err != nil {
			blog.Errorf("create module instance failed, bizID: %d, setID: %d, param: %#v, err: %v, rid: %s", bizID,
				setID, createModuleParam, err, kit.Rid)
			return setInstance, err
		}
	}

	return setInstance, nil
}

// DeleteSet delete set
func (s *set) DeleteSet(kit *rest.Kit, bizID int64, setIDs []int64) error {
	setCond := map[string]interface{}{common.BKAppIDField: bizID}
	if len(setIDs) > 0 {
		setCond[common.BKSetIDField] = map[string]interface{}{common.BKDBIN: setIDs}
	}

	// clear the module belong to deleted sets
	err := s.inst.DeleteInst(kit, common.BKInnerObjIDModule, setCond, true)
	if err != nil {
		blog.Errorf("delete module failed, err: %v, cond: %#v, rid: %s", err, setCond, kit.Rid)
		return err
	}

	taskCond := &metadata.DeleteOption{
		Condition: setCond,
	}
	if err = s.clientSet.TaskServer().Task().DeleteTask(kit.Ctx, kit.Header, taskCond); err != nil {
		blog.Errorf("failed to delete set sync task message failed, bizID: %d, setIDs: %#v, err: %v, rid: %s",
			bizID, setIDs, err, kit.Rid)
		return err
	}

	// clear the sets
	return s.inst.DeleteInst(kit, common.BKInnerObjIDSet, setCond, true)
}

// UpdateSet update set
func (s *set) UpdateSet(kit *rest.Kit, data mapstr.MapStr, bizID, setID int64) error {
	innerCond := mapstr.MapStr{
		common.BKAppIDField: bizID,
		common.BKSetIDField: setID,
	}

	data.Remove(common.MetadataField)
	data.Remove(common.BKAppIDField)
	data.Remove(common.BKSetIDField)
	data.Remove(common.BKSetTemplateIDField)

	err := s.inst.UpdateInst(kit, innerCond, data, common.BKInnerObjIDSet)
	if err != nil {
		blog.Errorf("update set instance failed, data: %#v, innerCond:%#v, err: %v, rid: %s", data, innerCond, err,
			kit.Rid)
		// return this duplicate error for unique validation failed
		if s.isSetDuplicateError(err) {
			blog.Errorf("update set instance failed, set name duplicated, rid: %s", kit.Rid)
			return kit.CCError.CCError(common.CCErrorSetNameDuplicated)
		}
		return err
	}

	return nil
}
